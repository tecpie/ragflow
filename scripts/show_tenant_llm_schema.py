import pymysql, os
conn = pymysql.connect(host=os.environ['MYSQL_HOST'], port=int(os.environ['MYSQL_PORT']), user=os.environ['MYSQL_USER'], password=os.environ['MYSQL_PASSWORD'], charset='utf8mb4')
with conn.cursor() as c:
    c.execute('SHOW COLUMNS FROM ragflow.tenant_llm')
    for row in c.fetchall():
        print(row)
conn.close()
