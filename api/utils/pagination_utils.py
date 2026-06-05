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

from common.config_utils import get_base_config
from common.constants import RAG_FLOW_SERVICE_NAME

_DEFAULT_REST_API_MAX_PAGE_SIZE = 100


def get_rest_api_max_page_size() -> int:
    return int(
        get_base_config(RAG_FLOW_SERVICE_NAME, {}).get(
            "rest_api_max_page_size", _DEFAULT_REST_API_MAX_PAGE_SIZE
        )
    )


def validate_rest_api_page_size(page_size: int) -> int:
    max_size = get_rest_api_max_page_size()
    if page_size > max_size:
        raise ValueError(f"page_size must be less than or equal to {max_size}")
    return page_size
