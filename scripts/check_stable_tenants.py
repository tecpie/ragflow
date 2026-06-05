import pymysql
import os

conn = pymysql.connect(
    host=os.environ.get("MYSQL_HOST", "172.16.0.20"),
    port=int(os.environ.get("MYSQL_PORT", 3306)),
    user=os.environ.get("MYSQL_USER", "tecpie"),
    password=os.environ.get("MYSQL_PASSWORD", ""),
    charset="utf8mb4",
)
with conn.cursor() as c:
    c.execute(
        "SELECT u.email, t.id, t.llm_id, t.embd_id FROM ragflow.user u "
        "JOIN ragflow.user_tenant ut ON ut.user_id=u.id "
        "JOIN ragflow.tenant t ON t.id=ut.tenant_id "
        "WHERE u.email IN ('admin@ragflow.io','platform@tecpie.com')"
    )
    for row in c.fetchall():
        email, tid = row[0], row[1]
        c.execute("SELECT COUNT(*) FROM ragflow.tenant_llm WHERE tenant_id=%s", (tid,))
        print("stable", row, "tenant_llm", c.fetchone()[0])
conn.close()
