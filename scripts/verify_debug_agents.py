import pymysql
import os

ids = [
    "d0f07bc055b411f1ab8757429e2e48f5",
    "478202d3e13e11f091b87a063ba180ad",
    "897324062fda11f082e633bfb2be14f7",
    "4cf9283e326a11f1bff30f290b8d52c7",
    "c2036654eba511f0a20fea224a7ee167",
    "2c5a5d4feb9f11f08a1bea224a7ee167",
]
conn = pymysql.connect(
    host=os.environ["MYSQL_HOST"],
    port=int(os.environ["MYSQL_PORT"]),
    user=os.environ["MYSQL_USER"],
    password=os.environ["MYSQL_PASSWORD"],
    charset="utf8mb4",
)
with conn.cursor() as c:
    for i in ids:
        c.execute(
            "SELECT id, title, user_id FROM ragflow_debug.user_canvas WHERE id=%s", (i,)
        )
        print(c.fetchone())
conn.close()
