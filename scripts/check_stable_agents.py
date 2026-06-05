import pymysql
import os

conn = pymysql.connect(
    host=os.environ["MYSQL_HOST"],
    port=int(os.environ["MYSQL_PORT"]),
    user=os.environ["MYSQL_USER"],
    password=os.environ["MYSQL_PASSWORD"],
    charset="utf8mb4",
)
ids = [
    "d0f07bc055b411f1ab8757429e2e48f5",
    "478202d3e13e11f091b87a063ba180ad",
    "897324062fda11f082e633bfb2be14f7",
    "4cf9283e326a11f1bff30f290b8d52c7",
    "c2036654eba511f0a20fea224a7ee167",
    "2c5a5d4feb9f11f08a1bea224a7ee167",
    "e1be7aa2eba511f08c5eea224a7ee167",
]
with conn.cursor() as c:
    for i in ids:
        c.execute("SELECT id, title, user_id FROM ragflow.user_canvas WHERE id=%s", (i,))
        r = c.fetchone()
        if r:
            print("canvas", r)
            continue
        c.execute("SELECT id, name, tenant_id FROM ragflow.knowledgebase WHERE id=%s", (i,))
        r = c.fetchone()
        print("kb" if r else "MISSING", r)
conn.close()
