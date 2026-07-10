#
#  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.
#
import json
import logging

from common.constants import ActiveStatusEnum, StatusEnum
from common.misc_utils import get_uuid
from api.db.joint_services.tenant_model_service import (
    delete_instances_by_provider_ids,
    delete_models_by_instance_ids,
)
from api.db.services.tenant_model_instance_service import TenantModelInstanceService
from api.db.services.tenant_model_provider_service import TenantModelProviderService
from api.db.services.tenant_model_service import TenantModelService

logger = logging.getLogger(__name__)

DEFAULT_INSTANCE_NAME = "default"


def _record_to_dict(record) -> dict:
    if isinstance(record, dict):
        return record
    if hasattr(record, "to_dict"):
        return record.to_dict()
    return dict(record.__data__)


def _ensure_provider(tenant_id: str, provider_name: str):
    provider = TenantModelProviderService.get_by_tenant_id_and_provider_name(tenant_id, provider_name)
    if provider:
        return provider
    provider_id = get_uuid()
    TenantModelProviderService.insert(
        id=provider_id,
        tenant_id=tenant_id,
        provider_name=provider_name,
    )
    return TenantModelProviderService.get_by_tenant_id_and_provider_name(tenant_id, provider_name)


def _build_instance_extra(api_base: str) -> str:
    if api_base:
        return json.dumps({"base_url": api_base})
    return "{}"


def _ensure_default_instance(provider_id: str, api_key: str, api_base: str = ""):
    instance = TenantModelInstanceService.get_by_provider_id_and_instance_name(
        provider_id, DEFAULT_INSTANCE_NAME
    )
    extra = _build_instance_extra(api_base)
    if instance:
        updates = {}
        if api_key is not None and instance.api_key != api_key:
            updates["api_key"] = api_key
        if api_base:
            extra_fields = json.loads(instance.extra) if instance.extra else {}
            if extra_fields.get("base_url") != api_base:
                extra_fields["base_url"] = api_base
                updates["extra"] = json.dumps(extra_fields)
        if updates:
            TenantModelInstanceService.update_by_id(instance.id, updates)
        return TenantModelInstanceService.get_by_provider_id_and_instance_name(
            provider_id, DEFAULT_INSTANCE_NAME
        )

    TenantModelInstanceService.insert(
        id=get_uuid(),
        provider_id=provider_id,
        instance_name=DEFAULT_INSTANCE_NAME,
        api_key=api_key or "",
        extra=extra,
        status=ActiveStatusEnum.ACTIVE.value,
    )
    return TenantModelInstanceService.get_by_provider_id_and_instance_name(
        provider_id, DEFAULT_INSTANCE_NAME
    )


def _sync_model_enable_state(provider_id: str, instance_id: str, model_name: str, model_type: str, enabled: bool):
    record = TenantModelService.get_by_provider_id_and_instance_id_and_model_name(
        provider_id, instance_id, model_name
    )
    if enabled:
        if record and record.status == ActiveStatusEnum.INACTIVE.value:
            TenantModelService.delete_by_id(record.id)
        return

    if record:
        TenantModelService.batch_update_model_status(
            [record.id],
            ActiveStatusEnum.INACTIVE.value,
        )
        return

    if not model_type:
        return

    TenantModelService.insert(
        id=get_uuid(),
        model_name=model_name,
        model_type=model_type,
        provider_id=provider_id,
        instance_id=instance_id,
        status=ActiveStatusEnum.INACTIVE.value,
    )


def sync_tenant_llm_from_record(record) -> None:
    data = _record_to_dict(record)
    tenant_id = data.get("tenant_id")
    provider_name = data.get("llm_factory")
    model_name = data.get("llm_name")
    model_type = data.get("model_type")
    api_key = data.get("api_key", "")
    api_base = data.get("api_base") or ""
    status = str(data.get("status", StatusEnum.VALID.value))

    if not tenant_id or not provider_name or not model_name:
        return

    provider = _ensure_provider(tenant_id, provider_name)
    if not provider:
        logger.warning(
            "Failed to sync tenant_llm provider tenant_id=%s provider=%s",
            tenant_id,
            provider_name,
        )
        return

    instance = _ensure_default_instance(provider.id, api_key, api_base)
    if not instance:
        logger.warning(
            "Failed to sync tenant_llm instance tenant_id=%s provider=%s",
            tenant_id,
            provider_name,
        )
        return

    _sync_model_enable_state(
        provider.id,
        instance.id,
        model_name,
        model_type,
        enabled=status == StatusEnum.VALID.value,
    )


def sync_tenant_llm_records(tenant_id: str) -> None:
    from api.db.services.tenant_llm_service import TenantLLMService

    for record in TenantLLMService.query(tenant_id=tenant_id):
        sync_tenant_llm_from_record(record)


def _delete_synced_model_records(tenant_id: str, provider_name: str, model_name: str) -> None:
    provider = TenantModelProviderService.get_by_tenant_id_and_provider_name(tenant_id, provider_name)
    if not provider:
        return

    instance = TenantModelInstanceService.get_by_provider_id_and_instance_name(
        provider.id, DEFAULT_INSTANCE_NAME
    )
    if not instance:
        return

    model = TenantModelService.get_by_provider_id_and_instance_id_and_model_name(
        provider.id, instance.id, model_name
    )
    if model:
        TenantModelService.delete_by_id(model.id)


def maybe_delete_provider_if_empty(tenant_id: str, provider_name: str) -> None:
    from api.db.services.tenant_llm_service import TenantLLMService

    if TenantLLMService.query(tenant_id=tenant_id, llm_factory=provider_name):
        return

    provider = TenantModelProviderService.get_by_tenant_id_and_provider_name(tenant_id, provider_name)
    if not provider:
        return

    instances = TenantModelInstanceService.get_all_by_provider_id(provider.id)
    instance_ids = [instance.id for instance in instances]
    if instance_ids:
        delete_models_by_instance_ids(instance_ids)
        delete_instances_by_provider_ids([provider.id])
    TenantModelProviderService.delete_by_tenant_id_and_provider_name(tenant_id, provider_name)


def sync_tenant_llm_delete_record(tenant_id: str, provider_name: str, model_name: str) -> None:
    _delete_synced_model_records(tenant_id, provider_name, model_name)
    maybe_delete_provider_if_empty(tenant_id, provider_name)


def sync_tenant_llm_factory_if_exists(tenant_id: str, provider_name: str) -> bool:
    from api.db.services.tenant_llm_service import TenantLLMService

    records = TenantLLMService.query(tenant_id=tenant_id, llm_factory=provider_name)
    if not records:
        return False

    for record in records:
        sync_tenant_llm_from_record(record)
    return True
