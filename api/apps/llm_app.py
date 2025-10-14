#
#  Copyright 2024 The InfiniFlow Authors. All Rights Reserved.
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
from flask_login import login_required, current_user
from api.utils.api_utils import validate_request
from api.apps.commons.llm_c import factories_c,set_api_key_c,add_llm_c,delete_llm_c,delete_factory_c,my_llms_c,list_app_c

@manager.route('/factories', methods=['GET'])  # noqa: F821
@login_required
def factories():
    return factories_c()


@manager.route('/set_api_key', methods=['POST'])  # noqa: F821
@login_required
@validate_request("llm_factory", "api_key")
def set_api_key():
    return set_api_key_c()


@manager.route('/add_llm', methods=['POST'])  # noqa: F821
@login_required
@validate_request("llm_factory")
def add_llm():
    return add_llm_c()


@manager.route('/delete_llm', methods=['POST'])  # noqa: F821
@login_required
@validate_request("llm_factory", "llm_name")
def delete_llm():
    return delete_llm_c()


@manager.route('/delete_factory', methods=['POST'])  # noqa: F821
@login_required
@validate_request("llm_factory")
def delete_factory():
    return delete_factory_c()


@manager.route('/my_llms', methods=['GET'])  # noqa: F821
@login_required
def my_llms():
    return my_llms_c()


@manager.route('/list', methods=['GET'])  # noqa: F821
@login_required
def list_app():
    return list_app_c()
