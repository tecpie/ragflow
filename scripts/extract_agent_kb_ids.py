import json
import re
import pymysql
import os

AGENT_IDS = [
    "d0f07bc055b411f1ab8757429e2e48f5",
    "478202d3e13e11f091b87a063ba180ad",
    "897324062fda11f082e633bfb2be14f7",
    "4cf9283e326a11f1bff30f290b8d52c7",
    "c2036654eba511f0a20fea224a7ee167",
    "2c5a5d4feb9f11f08a1bea224a7ee167",
]
pat = re.compile(r"[0-9a-f]{32}")

conn = pymysql.connect(
    host=os.environ["MYSQL_HOST"],
    port=int(os.environ["MYSQL_PORT"]),
    user=os.environ["MYSQL_USER"],
    password=os.environ["MYSQL_PASSWORD"],
    charset="utf8mb4",
)
kb_ids = set()
with conn.cursor() as c:
    placeholders = ", ".join(["%s"] * len(AGENT_IDS))
    c.execute(
        f"SELECT id, title, dsl FROM ragflow.user_canvas WHERE id IN ({placeholders})",
        AGENT_IDS,
    )
    for cid, title, dsl in c.fetchall():
        text = json.dumps(dsl, ensure_ascii=False)
        found = set(pat.findall(text)) - set(AGENT_IDS)
        print(title, "refs:", len(found))
        kb_ids.update(found)

print("all ref ids:", len(kb_ids))
with conn.cursor() as c:
    for kid in sorted(kb_ids):
        c.execute("SELECT id, name FROM ragflow.knowledgebase WHERE id=%s", (kid,))
        r = c.fetchone()
        print(" ", kid, "->", r[1] if r else "NOT_KB")
conn.close()
