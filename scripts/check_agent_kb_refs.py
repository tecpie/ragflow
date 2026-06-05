import json
import pymysql
import os

conn = pymysql.connect(
    host=os.environ["MYSQL_HOST"],
    port=int(os.environ["MYSQL_PORT"]),
    user=os.environ["MYSQL_USER"],
    password=os.environ["MYSQL_PASSWORD"],
    charset="utf8mb4",
)
kid = "e1be7aa2eba511f08c5eea224a7ee167"
with conn.cursor() as c:
    c.execute(
        "SELECT id, title, dsl FROM ragflow.user_canvas WHERE id IN (%s, %s)",
        ("c2036654eba511f0a20fea224a7ee167", "2c5a5d4feb9f11f08a1bea224a7ee167"),
    )
    for row in c.fetchall():
        cid, title, dsl = row
        s = json.dumps(dsl, ensure_ascii=False)
        print(title, "has kb ref:", kid in s)
        if kid in s:
            print("  found in dsl")
conn.close()
