#!/usr/bin/env python3
import os
import sys

sys.path.insert(0, "/ragflow")
os.environ.setdefault("PYTHONPATH", "/ragflow")

from api.db.db_models import DB

DB.connect()
for table in (
    "tenant_model_provider",
    "tenant_model_instance",
    "tenant_model",
):
    cur = DB.execute_sql(
        "SELECT COUNT(*) FROM information_schema.tables "
        "WHERE table_schema=DATABASE() AND table_name=%s",
        (table,),
    )
    print(f"table {table}:", cur.fetchone()[0])
cur = DB.execute_sql("SELECT COUNT(*) FROM tenant_model_provider")
print("provider_cnt:", cur.fetchone()[0])
cur = DB.execute_sql(
    "SELECT provider_name, COUNT(*) FROM tenant_model_provider GROUP BY provider_name"
)
print("providers:", list(cur.fetchall()))
cur = DB.execute_sql(
    "SELECT COUNT(*) FROM tenant_llm WHERE llm_factory='Tongyi-Qianwen'"
)
print("tongyi_tenant_llm:", cur.fetchone()[0])
cur = DB.execute_sql(
    "SELECT COUNT(*) FROM tenant_model_provider WHERE provider_name='Tongyi-Qianwen'"
)
print("tongyi_provider:", cur.fetchone()[0])
