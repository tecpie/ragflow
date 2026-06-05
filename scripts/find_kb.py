import pymysql
import os

kid = "e1be7aa2eba511f08c5eea224a7ee167"
conn = pymysql.connect(
    host=os.environ["MYSQL_HOST"],
    port=int(os.environ["MYSQL_PORT"]),
    user=os.environ["MYSQL_USER"],
    password=os.environ["MYSQL_PASSWORD"],
    charset="utf8mb4",
)
with conn.cursor() as c:
    c.execute("SELECT id, name, tenant_id FROM ragflow.knowledgebase WHERE id=%s", (kid,))
    print("exact:", c.fetchone())
    c.execute(
        "SELECT id, name, tenant_id FROM ragflow.knowledgebase WHERE id LIKE %s OR name LIKE %s LIMIT 10",
        (f"%{kid[:8]}%", "%审查%"),
    )
    print("like:", c.fetchall())
conn.close()
