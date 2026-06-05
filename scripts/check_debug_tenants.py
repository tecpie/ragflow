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
    c.execute("SELECT id, email, nickname FROM ragflow_debug.user")
    print("debug users:", c.fetchall())
    c.execute(
        "SELECT t.id, u.email FROM ragflow_debug.tenant t "
        "JOIN ragflow_debug.user_tenant ut ON ut.tenant_id=t.id "
        "JOIN ragflow_debug.user u ON u.id=ut.user_id"
    )
    for tid, email in c.fetchall():
        c.execute("SELECT COUNT(*) FROM ragflow_debug.tenant_llm WHERE tenant_id=%s", (tid,))
        print(f"tenant {tid} ({email}) tenant_llm:", c.fetchone()[0])
    c.execute("SELECT COUNT(*) FROM ragflow_debug.llm")
    print("llm catalog:", c.fetchone()[0])
conn.close()
