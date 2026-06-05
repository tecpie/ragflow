import pymysql, os
conn = pymysql.connect(host=os.environ['MYSQL_HOST'], port=int(os.environ['MYSQL_PORT']), user=os.environ['MYSQL_USER'], password=os.environ['MYSQL_PASSWORD'], charset='utf8mb4')
with conn.cursor() as c:
    c.execute("SELECT llm_id, embd_id, rerank_id FROM ragflow_debug.tenant t JOIN ragflow_debug.user u ON u.id=t.id JOIN ragflow_debug.user_tenant ut ON ut.tenant_id=t.id AND ut.user_id=u.id WHERE u.email='platform@tecpie.com'")
    print('debug tenant models:', c.fetchone())
    c.execute("SELECT COUNT(*) FROM ragflow_debug.tenant_llm WHERE tenant_id='ebc114845dc311f1a3712f9307738c16' AND api_key IS NOT NULL AND api_key != ''")
    print('tenant_llm with api_key:', c.fetchone()[0])
conn.close()
