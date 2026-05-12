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
import logging
import re

from quart import request, make_response
from api.apps import login_required
from api.db import FileType
from api.db.services.file2document_service import File2DocumentService
from api.db.services.file_service import FileService
from api.utils.api_utils import (
    add_tenant_id_to_kwargs,
    get_error_argument_result,
    get_error_data_result,
    get_json_result,
    get_result,
)
from common.constants import RetCode
from api.utils.validation_utils import (
    CreateFolderReq,
    DeleteFileReq,
    ListFileReq,
    MoveFileReq,
    ShareFileReq,
    UpdateFileInfoReq,
    validate_and_parse_json_request,
    validate_and_parse_request_args,
)
from api.utils.web_utils import CONTENT_TYPE_MAP, apply_safe_file_response_headers
from common import settings
from common.misc_utils import thread_pool_exec
from api.apps.services import file_api_service


@manager.route("/files", methods=["POST"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def create_or_upload(tenant_id: str = None):
    """
    Upload files or create a folder.
    ---
    tags:
      - Files
    security:
      - ApiKeyAuth: []
    parameters:
      - in: header
        name: Authorization
        type: string
        required: true
        description: Bearer token for authentication.
    responses:
      200:
        description: Successful operation.
    """
    content_type = request.content_type or ""
    try:
        if "multipart/form-data" in content_type:
            form = await request.form
            pf_id = form.get("parent_id")
            files = await request.files
            if 'file' not in files:
                return get_error_argument_result("No file part!")
            file_objs = files.getlist('file')
            for file_obj in file_objs:
                if file_obj.filename == '':
                    return get_error_argument_result("No file selected!")

            success, result = await file_api_service.upload_file(tenant_id, pf_id, file_objs)
            if success:
                return get_result(data=result)
            else:
                return get_error_data_result(message=result)
        else:
            req, err = await validate_and_parse_json_request(request, CreateFolderReq)
            if err is not None:
                return get_error_argument_result(err)

            success, result = await file_api_service.create_folder(
                tenant_id, req["name"], req.get("parent_id"), req.get("type")
            )
            if success:
                return get_result(data=result)
            else:
                return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files", methods=["GET"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def list_files(tenant_id: str = None):
    """
    List files under a folder.
    ---
    tags:
      - Files
    security:
      - ApiKeyAuth: []
    parameters:
      - in: query
        name: parent_id
        type: string
        description: Folder ID to list files from.
      - in: query
        name: keywords
        type: string
        description: Search keyword filter.
      - in: query
        name: page
        type: integer
        default: 1
      - in: query
        name: page_size
        type: integer
        default: 15
      - in: query
        name: orderby
        type: string
        default: "create_time"
      - in: query
        name: desc
        type: boolean
        default: true
    responses:
      200:
        description: Successful operation.
    """
    args, err = validate_and_parse_request_args(request, ListFileReq)
    if err is not None:
        return get_error_argument_result(err)

    try:
        success, result = file_api_service.list_files(tenant_id, args)
        if success:
            return get_result(data=result)
        else:
            return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files", methods=["DELETE"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def delete(tenant_id: str = None):
    """
    Delete files.
    ---
    tags:
      - Files
    security:
      - ApiKeyAuth: []
    parameters:
      - in: body
        name: body
        required: true
        schema:
          type: object
          required:
            - ids
          properties:
            ids:
              type: array
              items:
                type: string
              description: List of file IDs to delete.
    responses:
      200:
        description: Successful operation.
    """
    req, err = await validate_and_parse_json_request(request, DeleteFileReq)
    if err is not None:
        return get_error_argument_result(err)

    try:
        # Get Authorization header to pass to Go backend
        auth_header = request.headers.get("Authorization", "")
        success, result = await file_api_service.delete_files(tenant_id, req["ids"], auth_header)
        if success:
            return get_result(data=result)
        else:
            if isinstance(result, dict):
                success_count = result.get("success_count", 0)
                errors = result.get("errors", [])
                return get_json_result(
                    code=RetCode.DATA_ERROR,
                    message=f"Partially deleted {success_count} files with {len(errors)} errors"
                    if success_count > 0
                    else f"Deleted files failed with {len(errors)} errors",
                    data=result,
                )
            return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")



@manager.route("/files/move", methods=["POST"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def move(tenant_id: str = None):
    """
    Move and/or rename files. Follows Linux mv semantics:
    at least one of dest_file_id or new_name must be provided.
    - dest_file_id only: move files to a new folder (names unchanged).
    - new_name only: rename a single file in place (no storage operation).
    - both: move and rename simultaneously.
    ---
    tags:
      - Files
    security:
      - ApiKeyAuth: []
    parameters:
      - in: body
        name: body
        required: true
        schema:
          type: object
          required:
            - src_file_ids
          properties:
            src_file_ids:
              type: array
              items:
                type: string
              description: List of source file IDs. Required.
            dest_file_id:
              type: string
              description: Destination folder ID. Optional; omit to rename in place.
            new_name:
              type: string
              description: New file name. Optional; only valid for a single source file.
    responses:
      200:
        description: Successful operation.
    """
    req, err = await validate_and_parse_json_request(request, MoveFileReq)
    if err is not None:
        return get_error_argument_result(err)

    try:
        success, result = await file_api_service.move_files(
            tenant_id, req["src_file_ids"], req.get("dest_file_id"), req.get("new_name")
        )
        if success:
            return get_result(data=result)
        else:
            return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files/<file_id>", methods=["GET"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def download(tenant_id: str = None, file_id: str = None):
    """
    Download a file.
    ---
    tags:
      - Files
    security:
      - ApiKeyAuth: []
    produces:
      - application/octet-stream
    parameters:
      - in: path
        name: file_id
        type: string
        required: true
        description: File ID to download.
    responses:
      200:
        description: File stream.
    """
    try:
        success, result = file_api_service.get_file_content(tenant_id, file_id)
        if not success:
            return get_error_data_result(message=result)

        file = result
        blob = await thread_pool_exec(settings.STORAGE_IMPL.get, file.parent_id, file.location)
        if not blob:
            b, n = File2DocumentService.get_storage_address(file_id=file_id)
            blob = await thread_pool_exec(settings.STORAGE_IMPL.get, b, n)

        response = await make_response(blob)
        ext = re.search(r"\.([^.]+)$", file.name.lower())
        ext = ext.group(1) if ext else None
        content_type = None
        if ext:
            fallback_prefix = "image" if file.type == FileType.VISUAL.value else "application"
            content_type = CONTENT_TYPE_MAP.get(ext, f"{fallback_prefix}/{ext}")
        apply_safe_file_response_headers(response, content_type, ext)
        return response
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files/<file_id>/parent", methods=["GET"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def parent_folder(tenant_id: str = None, file_id: str = None):
    """
    Get parent folder of a file.
    ---
    tags:
      - Files
    security:
      - ApiKeyAuth: []
    parameters:
      - in: path
        name: file_id
        type: string
        required: true
    responses:
      200:
        description: Parent folder information.
    """
    try:
        success, result = file_api_service.get_parent_folder(file_id, user_id=tenant_id)
        if success:
            return get_result(data=result)
        else:
            return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files/<file_id>/ancestors", methods=["GET"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def ancestors(tenant_id: str = None, file_id: str = None):
    """
    Get all ancestor folders of a file.
    ---
    tags:
      - Files
    security:
      - ApiKeyAuth: []
    parameters:
      - in: path
        name: file_id
        type: string
        required: true
    responses:
      200:
        description: List of ancestor folders.
    """
    try:
        success, result = file_api_service.get_all_parent_folders(file_id, user_id=tenant_id)
        if success:
            return get_result(data=result)
        else:
            return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")

@manager.route("/files/upload_info", methods=["POST"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def upload_info(tenant_id: str = None):
    """
    Upload runtime file metadata for SDK chat completions.
    ---
    tags:
      - File
    security:
      - ApiKeyAuth: []
    parameters:
      - in: formData
        name: file
        type: file
        required: false
        description: File(s) to upload as runtime attachments.
      - in: query
        name: url
        type: string
        required: false
        description: Optional URL to fetch and convert into a runtime attachment.
    responses:
      200:
        description: Runtime attachment descriptor(s) for the `files` field in completions requests.
    """
    files = await request.files
    file_objs = files.getlist("file") if files and files.get("file") else []
    url = request.args.get("url")

    if file_objs and url:
        return get_error_argument_result("Provide either multipart file(s) or ?url=..., not both.")

    if not file_objs and not url:
        return get_error_argument_result("Missing input: provide multipart file(s) or url")

    try:
        if url and not file_objs:
            result = await thread_pool_exec(FileService.upload_info, tenant_id, None, url)
            return get_result(data=result)

        if len(file_objs) == 1:
            result = await thread_pool_exec(FileService.upload_info, tenant_id, file_objs[0], None)
            return get_result(data=result)

        results = []
        for f in file_objs:
            results.append(await thread_pool_exec(FileService.upload_info, tenant_id, f, None))
        return get_result(data=results)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files/download/<attachment_id>", methods=["GET"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def download_attachment(tenant_id: str = None, attachment_id: str = None):
    try:
        ext = request.args.get("ext", "markdown")
        data = await thread_pool_exec(settings.STORAGE_IMPL.get, tenant_id, attachment_id)
        response = await make_response(data)
        content_type = CONTENT_TYPE_MAP.get(ext, f"application/{ext}")
        apply_safe_file_response_headers(response, content_type, ext)

        return response

    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files/convert", methods=["POST"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def convert(tenant_id: str = None):
    req, err = await validate_and_parse_json_request(request, ShareFileReq)
    if err is not None:
        return get_error_argument_result(err)

    try:
        success, result = await file_api_service.convert_files(tenant_id, req["file_ids"], req["kb_ids"])
        if success:
            return get_result(data=result)
        return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")

@manager.route("/files/share", methods=["POST"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def share(tenant_id: str = None):
    """
    Share files to target knowledge bases. Does not remove documents from source KBs.
    If a file already exists in a target KB, that target is skipped.
    ---
    tags:
      - File
    security:
      - ApiKeyAuth: []
    parameters:
      - in: body
        name: body
        required: true
        schema:
          type: object
          properties:
            file_ids:
              type: array
              items:
                type: string
              description: List of file IDs to share
            kb_ids:
              type: array
              items:
                type: string
              description: List of knowledge base IDs to share to
    responses:
      200:
        description: New file2document records created (existing in target KB are skipped)
    """
    req, err = await validate_and_parse_json_request(request, ShareFileReq)
    if err is not None:
        return get_error_argument_result(err)
    try:
        success, result = await file_api_service.share_files(tenant_id, req["file_ids"], req["kb_ids"])
        if success:
            return get_result(data=result)
        return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")


@manager.route("/files/<file_id>", methods=["PUT"])  # noqa: F821
@login_required
@add_tenant_id_to_kwargs
async def update_file_info(tenant_id: str = None, file_id: str = None):
    """
    Update file name, status, and created_by, along with associated documents info.
    ---
    tags:
      - File
    security:
      - ApiKeyAuth: []
    parameters:
      - in: body
        name: body
        description: File update parameters
        required: true
        schema:
          type: object
          properties:
            file_id:
              type: string
              description: Target file ID
            name:
              type: string
              description: New file name (optional)
            status:
              type: integer
              description: New file status (optional)
            created_by:
              type: string
              description: New created_by value (optional)
    responses:
      200:
        description: File updated successfully

    """
    req, err = await validate_and_parse_json_request(
        request, UpdateFileInfoReq, extras={"file_id": file_id}, exclude_unset=True
    )
    if err is not None:
        return get_error_argument_result(err)
    try:
        success, result = await file_api_service.update_file_info(tenant_id, file_id, req)
        if success:
            return get_result(data=result)
        return get_error_data_result(message=result)
    except Exception as e:
        logging.exception(e)
        return get_error_data_result(message="Internal server error")
