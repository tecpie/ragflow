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
import os
import pathlib
from pathlib import Path
import asyncio

from api.common.check_team_permission import check_file_team_permission
from api.db import FileType
from api.db.services import duplicate_name
from api.db.services.doc_metadata_service import DocMetadataService
from api.db.services.document_service import DocumentService
from api.db.services.file2document_service import File2DocumentService
from api.db.services.file_service import FileService
from api.db.services.knowledgebase_service import KnowledgebaseService
from api.utils.file_utils import filename_type
from common import settings
from common.constants import FileSource
from common.misc_utils import get_uuid, thread_pool_exec


async def upload_file(tenant_id: str, pf_id: str, file_objs: list):
    """
    Upload files to a folder.

    :param tenant_id: tenant ID
    :param pf_id: parent folder ID
    :param file_objs: list of file objects from request
    :return: (success, result_list) or (success, error_message)
    """
    if not pf_id:
        root_folder = FileService.get_root_folder(tenant_id)
        pf_id = root_folder["id"]

    e, pf_folder = FileService.get_by_id(pf_id)
    if not e:
        return False, "Can't find this folder!"

    file_res = []
    for file_obj in file_objs:
        MAX_FILE_NUM_PER_USER = int(os.environ.get('MAX_FILE_NUM_PER_USER', 0))
        if 0 < MAX_FILE_NUM_PER_USER <= await thread_pool_exec(DocumentService.get_doc_count, tenant_id):
            return False, "Exceed the maximum file number of a free user!"

        if not file_obj.filename:
            file_obj_names = [pf_folder.name, file_obj.filename]
        else:
            full_path = '/' + file_obj.filename
            file_obj_names = full_path.split('/')
        file_len = len(file_obj_names)

        file_id_list = await thread_pool_exec(FileService.get_id_list_by_id, pf_id, file_obj_names, 1, [pf_id])
        len_id_list = len(file_id_list)

        if file_len != len_id_list:
            e, file = await thread_pool_exec(FileService.get_by_id, file_id_list[len_id_list - 1])
            if not e:
                return False, "Folder not found!"
            last_folder = await thread_pool_exec(
                FileService.create_folder, file, file_id_list[len_id_list - 1], file_obj_names, len_id_list
            )
        else:
            e, file = await thread_pool_exec(FileService.get_by_id, file_id_list[len_id_list - 2])
            if not e:
                return False, "Folder not found!"
            last_folder = await thread_pool_exec(
                FileService.create_folder, file, file_id_list[len_id_list - 2], file_obj_names, len_id_list
            )

        filetype = filename_type(file_obj_names[file_len - 1])
        blob = await thread_pool_exec(file_obj.read)
        filename = await thread_pool_exec(
            duplicate_name, FileService.query, name=file_obj_names[file_len - 1], parent_id=last_folder.id
        )
        location = filename
        await thread_pool_exec(settings.STORAGE_IMPL.put, last_folder.id, location, blob)
        file_data = {
            "id": get_uuid(),
            "parent_id": last_folder.id,
            "tenant_id": tenant_id,
            "created_by": tenant_id,
            "type": filetype,
            "name": filename,
            "location": location,
            "size": len(blob),
        }
        inserted = await thread_pool_exec(FileService.insert, file_data)
        file_res.append(inserted.to_json())

    return True, file_res


async def create_folder(tenant_id: str, name: str, pf_id: str = None, file_type: str = None):
    """
    Create a new folder or virtual file.

    :param tenant_id: tenant ID
    :param name: folder name
    :param pf_id: parent folder ID
    :param file_type: file type (folder or virtual)
    :return: (success, result) or (success, error_message)
    """
    if not pf_id:
        root_folder = FileService.get_root_folder(tenant_id)
        pf_id = root_folder["id"]

    if not FileService.is_parent_folder_exist(pf_id):
        return False, "Parent Folder Doesn't Exist!"
    if FileService.query(name=name, parent_id=pf_id):
        return False, "Duplicated folder name in the same folder."

    if (file_type or "").lower() == FileType.FOLDER.value:
        ft = FileType.FOLDER.value
    else:
        ft = FileType.VIRTUAL.value

    file = FileService.insert({
        "id": get_uuid(),
        "parent_id": pf_id,
        "tenant_id": tenant_id,
        "created_by": tenant_id,
        "name": name,
        "location": "",
        "size": 0,
        "type": ft,
    })
    return True, file.to_json()


def list_files(tenant_id: str, args: dict):
    """
    List files under a folder.

    :param tenant_id: tenant ID
    :param args: query arguments (parent_id, keywords, page, page_size, orderby, desc)
    :return: (success, result) or (success, error_message)
    """
    pf_id = args.get("parent_id")
    keywords = args.get("keywords", "")
    page_number = int(args.get("page", 1))
    items_per_page = int(args.get("page_size", 15))
    orderby = args.get("orderby", "create_time")
    desc = args.get("desc", True)

    if not pf_id:
        root_folder = FileService.get_root_folder(tenant_id)
        pf_id = root_folder["id"]
        FileService.init_knowledgebase_docs(pf_id, tenant_id)

    e, file = FileService.get_by_id(pf_id)
    if not e:
        return False, "Folder not found!"

    files, total = FileService.get_by_pf_id(tenant_id, pf_id, page_number, items_per_page, orderby, desc, keywords)

    parent_folder = FileService.get_parent_folder(pf_id)
    if not parent_folder:
        return False, "File not found!"

    return True, {"total": total, "files": files, "parent_folder": parent_folder.to_json()}



def get_parent_folder(file_id: str):
    """
    Get parent folder of a file.

    :param file_id: file ID
    :return: (success, result) or (success, error_message)
    """
    e, file = FileService.get_by_id(file_id)
    if not e:
        return False, "Folder not found!"

    parent_folder = FileService.get_parent_folder(file_id)
    return True, {"parent_folder": parent_folder.to_json()}


def get_all_parent_folders(file_id: str):
    """
    Get all ancestor folders of a file.

    :param file_id: file ID
    :return: (success, result) or (success, error_message)
    """
    e, file = FileService.get_by_id(file_id)
    if not e:
        return False, "Folder not found!"

    parent_folders = FileService.get_all_parent_folders(file_id)
    return True, {"parent_folders": [pf.to_json() for pf in parent_folders]}


async def delete_files(uid: str, file_ids: list):
    """
    Delete files/folders with team permission check and recursive deletion.

    :param uid: user ID
    :param file_ids: list of file IDs to delete
    :return: (success, result) or (success, error_message)
    """
    def _delete_single_file(file):
        try:
            if file.location:
                settings.STORAGE_IMPL.rm(file.parent_id, file.location)
        except Exception as e:
            logging.exception(f"Fail to remove object: {file.parent_id}/{file.location}, error: {e}")

        informs = File2DocumentService.get_by_file_id(file.id)
        for inform in informs:
            doc_id = inform.document_id
            e, doc = DocumentService.get_by_id(doc_id)
            if e and doc:
                tenant_id = DocumentService.get_tenant_id(doc_id)
                if tenant_id:
                    DocumentService.remove_document(doc, tenant_id)
            File2DocumentService.delete_by_file_id(file.id)

        FileService.delete(file)

    def _delete_folder_recursive(folder, tenant_id):
        sub_files = FileService.list_all_files_by_parent_id(folder.id)
        for sub_file in sub_files:
            if sub_file.type == FileType.FOLDER.value:
                _delete_folder_recursive(sub_file, tenant_id)
            else:
                _delete_single_file(sub_file)
        FileService.delete(folder)

    def _rm_sync():
        for file_id in file_ids:
            e, file = FileService.get_by_id(file_id)
            if not e or not file:
                return False, "File or Folder not found!"
            if not file.tenant_id:
                return False, "Tenant not found!"
            if not check_file_team_permission(file, uid):
                return False, "No authorization."

            if file.type == FileType.FOLDER.value:
                _delete_folder_recursive(file, uid)
                continue

            _delete_single_file(file)

        return True, True

    return await thread_pool_exec(_rm_sync)


async def move_files(uid: str, src_file_ids: list, dest_file_id: str = None, new_name: str = None):
    """
    Move and/or rename files. Follows Linux mv semantics:
    - new_name only: rename in place (no storage operation)
    - dest_file_id only: move to new folder (keep names)
    - both: move and rename simultaneously

    :param uid: user ID
    :param src_file_ids: list of source file IDs
    :param dest_file_id: destination folder ID (optional)
    :param new_name: new name for the file (optional, single file only)
    :return: (success, result) or (success, error_message)
    """
    files = FileService.get_by_ids(src_file_ids)
    if not files:
        return False, "Source files not found!"

    files_dict = {f.id: f for f in files}

    for file_id in src_file_ids:
        file = files_dict.get(file_id)
        if not file:
            return False, "File or folder not found!"
        if not file.tenant_id:
            return False, "Tenant not found!"
        if not check_file_team_permission(file, uid):
            return False, "No authorization."

    dest_folder = None
    if dest_file_id:
        ok, dest_folder = FileService.get_by_id(dest_file_id)
        if not ok or not dest_folder:
            return False, "Parent folder not found!"

    if new_name:
        file = files_dict[src_file_ids[0]]
        if file.type != FileType.FOLDER.value and \
                pathlib.Path(new_name.lower()).suffix != pathlib.Path(file.name.lower()).suffix:
            return False, "The extension of file can't be changed"
        target_parent_id = dest_folder.id if dest_folder else file.parent_id
        for f in FileService.query(name=new_name, parent_id=target_parent_id):
            if f.name == new_name:
                return False, "Duplicated file name in the same folder."

    def _move_entry_recursive(source_file_entry, dest_folder_entry, override_name=None):
        effective_name = override_name or source_file_entry.name

        if source_file_entry.type == FileType.FOLDER.value:
            existing_folder = FileService.query(name=effective_name, parent_id=dest_folder_entry.id)
            if existing_folder:
                new_folder = existing_folder[0]
            else:
                new_folder = FileService.insert({
                    "id": get_uuid(),
                    "parent_id": dest_folder_entry.id,
                    "tenant_id": source_file_entry.tenant_id,
                    "created_by": source_file_entry.tenant_id,
                    "name": effective_name,
                    "location": "",
                    "size": 0,
                    "type": FileType.FOLDER.value,
                })

            sub_files = FileService.list_all_files_by_parent_id(source_file_entry.id)
            for sub_file in sub_files:
                _move_entry_recursive(sub_file, new_folder)

            FileService.delete_by_id(source_file_entry.id)
            return

        # Non-folder file
        need_storage_move = dest_folder_entry.id != source_file_entry.parent_id
        updates = {}

        if need_storage_move:
            new_location = effective_name
            while settings.STORAGE_IMPL.obj_exist(dest_folder_entry.id, new_location):
                new_location += "_"
            try:
                settings.STORAGE_IMPL.move(
                    source_file_entry.parent_id, source_file_entry.location,
                    dest_folder_entry.id, new_location,
                )
            except Exception as storage_err:
                raise RuntimeError(f"Move file failed at storage layer: {str(storage_err)}")
            updates["parent_id"] = dest_folder_entry.id
            updates["location"] = new_location

        if override_name:
            updates["name"] = override_name

        if updates:
            FileService.update_by_id(source_file_entry.id, updates)

        if override_name:
            informs = File2DocumentService.get_by_file_id(source_file_entry.id)
            if informs:
                if not DocumentService.update_by_id(informs[0].document_id, {"name": override_name}):
                    raise RuntimeError("Database error (Document rename)!")

    def _move_or_rename_sync():
        if dest_folder:
            for file in files:
                _move_entry_recursive(file, dest_folder, override_name=new_name)
        else:
            # Pure rename: no storage operation needed
            file = files[0]
            if not FileService.update_by_id(file.id, {"name": new_name}):
                return False, "Database error (File rename)!"
            informs = File2DocumentService.get_by_file_id(file.id)
            if informs:
                if not DocumentService.update_by_id(informs[0].document_id, {"name": new_name}):
                    return False, "Database error (Document rename)!"
        return True, True

    return await thread_pool_exec(_move_or_rename_sync)


def get_file_content(uid: str, file_id: str):
    """
    Get file content and metadata for download.

    :param uid: user ID
    :param file_id: file ID
    :return: (success, (blob, file_obj)) or (success, error_message)
    """
    e, file = FileService.get_by_id(file_id)
    if not e:
        return False, "Document not found!"
    if not check_file_team_permission(file, uid):
        return False, "No authorization."
    return True, file


async def share_files(uid: str, file_ids: list[str], kb_ids: list[str]):
    """
    Share files to target knowledge bases without removing source mappings.

    Existing file-document mappings in target KB are skipped.
    """
    files = FileService.get_by_ids(file_ids)
    if not files:
        return False, "Source files not found!"
    files_dict = {f.id: f for f in files}

    def _share_sync():
        file2documents = []
        for file_id in file_ids:
            file = files_dict.get(file_id)
            if not file:
                return False, "File not found!"
            if not check_file_team_permission(file, uid):
                return False, "No authorization."

            target_file_ids = [file_id]
            if file.type == FileType.FOLDER.value:
                target_file_ids = FileService.get_all_innermost_file_ids(file_id, [])

            for target_file_id in target_file_ids:
                ok, target_file = FileService.get_by_id(target_file_id)
                if not ok or not target_file:
                    return False, "Can't find this file!"

                existing_kb_ids = set()
                for inform in File2DocumentService.get_by_file_id(target_file_id):
                    exists, doc = DocumentService.get_by_id(inform.document_id)
                    if exists and doc:
                        existing_kb_ids.add(doc.kb_id)

                for kb_id in kb_ids:
                    if kb_id in existing_kb_ids:
                        continue

                    exists, kb = KnowledgebaseService.get_by_id(kb_id)
                    if not exists or not kb:
                        return False, "Can't find this dataset!"

                    doc = DocumentService.insert({
                        "id": get_uuid(),
                        "kb_id": kb.id,
                        "parser_id": FileService.get_parser(target_file.type, target_file.name, kb.parser_id),
                        "parser_config": kb.parser_config,
                        "created_by": uid,
                        "type": target_file.type,
                        "name": target_file.name,
                        "suffix": Path(target_file.name).suffix.lstrip("."),
                        "location": target_file.location,
                        "size": target_file.size,
                    })
                    file2document = File2DocumentService.insert({
                        "id": get_uuid(),
                        "file_id": target_file_id,
                        "document_id": doc.id,
                    })
                    file2documents.append(file2document.to_json())
        return True, file2documents

    return await thread_pool_exec(_share_sync)


def _convert_files_sync(file_ids: list[str], kb_ids: list[str], uid: str):
    files = FileService.get_by_ids(file_ids)
    files_dict = {f.id: f for f in files}

    for file_id in file_ids:
        if not files_dict.get(file_id):
            return False, "File not found!"
        if not check_file_team_permission(files_dict[file_id], uid):
            return False, "No authorization."

    for kb_id in kb_ids:
        exists, _ = KnowledgebaseService.get_by_id(kb_id)
        if not exists:
            return False, "Can't find this dataset!"

    file2documents = []
    for file_id in file_ids:
        file = files_dict[file_id]
        target_file_ids = [file_id]
        if file.type == FileType.FOLDER.value:
            target_file_ids = FileService.get_all_innermost_file_ids(file_id, [])

        for target_file_id in target_file_ids:
            informs = File2DocumentService.get_by_file_id(target_file_id)
            for inform in informs:
                doc_id = inform.document_id
                ok, doc = DocumentService.get_by_id(doc_id)
                if not ok:
                    continue
                doc_tenant_id = DocumentService.get_tenant_id(doc_id)
                if not doc_tenant_id:
                    continue
                if not DocumentService.remove_document(doc, doc_tenant_id):
                    return False, "Database error (Document removal)!"

            File2DocumentService.delete_by_file_id(target_file_id)

            ok, target_file = FileService.get_by_id(target_file_id)
            if not ok or not target_file:
                return False, "Can't find this file!"

            for kb_id in kb_ids:
                exists, kb = KnowledgebaseService.get_by_id(kb_id)
                if not exists or not kb:
                    return False, "Can't find this dataset!"

                doc = DocumentService.insert({
                    "id": get_uuid(),
                    "kb_id": kb.id,
                    "parser_id": FileService.get_parser(target_file.type, target_file.name, kb.parser_id),
                    "pipeline_id": kb.pipeline_id,
                    "parser_config": kb.parser_config,
                    "created_by": uid,
                    "type": target_file.type,
                    "name": target_file.name,
                    "suffix": Path(target_file.name).suffix.lstrip("."),
                    "location": target_file.location,
                    "size": target_file.size,
                })
                file2document = File2DocumentService.insert({
                    "id": get_uuid(),
                    "file_id": target_file_id,
                    "document_id": doc.id,
                })
                file2documents.append(file2document.to_json())

    return True, file2documents


async def convert_files(uid: str, file_ids: list[str], kb_ids: list[str], run_async: bool = False):
    """
    Convert files/folders to documents in target knowledge bases.

    :param uid: user ID
    :param file_ids: source file IDs
    :param kb_ids: target knowledge base IDs
    :param run_async: whether to run conversion in background
    :return: (success, result_or_bool) or (success, error_message)
    """
    if run_async:
        loop = asyncio.get_running_loop()
        future = loop.run_in_executor(None, _convert_files_sync, file_ids, kb_ids, uid)

        def _done_callback(f):
            try:
                success, result = f.result()
                if not success:
                    logging.error("convert_files background failed: %s", result)
            except Exception as exc:
                logging.exception("convert_files background crashed: %s", exc)

        future.add_done_callback(_done_callback)
        return True, True

    return await thread_pool_exec(_convert_files_sync, file_ids, kb_ids, uid)


async def update_file_info(uid: str, file_id: str, req: dict):
    """
    Update file fields and synchronize related document fields.
    """
    def _update_sync():
        ok, file = FileService.get_by_id(file_id)
        if not ok or not file:
            return False, "File not found!"
        if not check_file_team_permission(file, uid):
            return False, "No authorization."

        new_name = req.get("name")
        new_status = req.get("status")
        new_created_by = req.get("created_by")
        new_meta_fields = req.get("meta_fields")

        file_update_data = {}
        document_update_data = {}

        if new_name:
            file_update_data["name"] = new_name
            document_update_data["name"] = new_name

        if new_status is not None and new_status > file.status:
            file_update_data["status"] = new_status

        if new_created_by:
            file_update_data["created_by"] = new_created_by
            document_update_data["created_by"] = new_created_by

        if file_update_data and not FileService.update_by_id(file_id, file_update_data):
            return False, "Database error (File update)!"

        informs = File2DocumentService.get_by_file_id(file_id)
        for inform in informs:
            doc_id = inform.document_id
            doc_ok, _ = DocumentService.get_by_id(doc_id)
            if not doc_ok:
                continue

            if document_update_data and not DocumentService.update_by_id(doc_id, document_update_data):
                return False, f"Database error (Document {doc_id} update)!"
            if new_meta_fields is not None and not DocMetadataService.update_document_metadata(doc_id, new_meta_fields):
                return False, f"Database error (Document {doc_id} metadata update)!"
        return True, True

    return await thread_pool_exec(_update_sync)
