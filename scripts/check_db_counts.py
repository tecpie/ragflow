import pymysql
import os

conn = pymysql.connect(
    host=os.environ.get("MYSQL_HOST", "172.16.0.20"),
    port=int(os.environ.get("MYSQL_PORT", 3306)),
    user=os.environ.get("MYSQL_USER", "tecpie"),
    password=os.environ.get("MYSQL_PASSWORD", ""),
    charset="utf8mb4",
)
tables = ["llm", "llm_factories", "tenant_llm", "user", "tenant"]
for db in ["ragflow", "ragflow_debug"]:
    print("===", db, "===")
    with conn.cursor() as c:
        for t in tables:
            try:
                c.execute(f"SELECT COUNT(*) FROM `{db}`.`{t}`")
                print(t, c.fetchone()[0])
            except Exception as e:
                print(t, "ERR", e)
conn.close()
