-- 1. 数据库连接权限
GRANT CONNECT ON DATABASE zke TO zke_dev;

-- 2. schema 权限：USAGE（查看） + CREATE（重点，用来建schema_migrations）
GRANT USAGE, CREATE ON SCHEMA public TO zke_dev;

-- 3. 当前已有表读写权限
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO zke_dev;

-- 4. 序列权限（自增主键）
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO zke_dev;

-- 5. 默认权限：以后新建表、序列自动授权
ALTER DEFAULT PRIVILEGES IN SCHEMA public
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO zke_dev;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
GRANT USAGE, SELECT ON SEQUENCES TO zke_dev;



SELECT
has_database_privilege('zke_dev','zke','CONNECT') AS connect_ok,
has_schema_privilege('zke_dev','public','USAGE') AS schema_usage_ok,
has_schema_privilege('zke_dev','public','CREATE') AS schema_create_ok;

