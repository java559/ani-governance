-- QUOTA-CATALOG-01 版本数据脚本：新增 CPU / 内存 / 存储三项配额目录。
-- 本脚本只新增目录项，不改代码、不启用执行能力、不产生任何占用。
-- 三项编码均不在旧 legacy 映射内，也不在 GPU 能力注册内，
-- 因此 API 读取时 enforcement 由代码统一派生为 NOT_ENABLED（仅展示，不拦截）。
-- 执行方式：显式版本数据步骤，psql -v ON_ERROR_STOP=1 -f；不得在服务启动时执行。
-- 幂等：已存在同 code 行时跳过插入；若既有定义与本脚本不一致则整笔失败，不覆盖旧行。

BEGIN;

INSERT INTO sys_quota_definitions (code, display_name, unit, accounting_kind, created_at)
SELECT v.code, v.display_name, v.unit, v.kind, now()
FROM (VALUES
  ('cpu.cores',   'CPU cores',        'core', 'CONCURRENT'),
  ('memory.mib',  'Memory',           'MiB',  'CONCURRENT'),
  ('storage.gib', 'Storage capacity', 'GiB',  'CONCURRENT')
) AS v(code, display_name, unit, kind)
WHERE NOT EXISTS (
  SELECT 1 FROM sys_quota_definitions d WHERE d.code = v.code
);

-- 已存在的定义必须与本脚本一致；不一致直接失败，避免静默保留不同语义。
DO $$
DECLARE mismatched bigint;
BEGIN
  SELECT count(*) INTO mismatched
  FROM (VALUES
    ('cpu.cores',   'CPU cores',        'core', 'CONCURRENT'),
    ('memory.mib',  'Memory',           'MiB',  'CONCURRENT'),
    ('storage.gib', 'Storage capacity', 'GiB',  'CONCURRENT')
  ) AS v(code, display_name, unit, kind)
  JOIN sys_quota_definitions d ON d.code = v.code
  WHERE d.display_name <> v.display_name
     OR d.unit <> v.unit
     OR d.accounting_kind <> v.kind;

  IF mismatched > 0 THEN
    RAISE EXCEPTION 'quota catalog mismatch: % existing definition(s) differ from this script', mismatched;
  END IF;
END $$;

-- 输出本次目标目录项的最终状态，供执行记录留证。
SELECT id, code, display_name, unit, accounting_kind
FROM sys_quota_definitions
WHERE code IN ('cpu.cores', 'memory.mib', 'storage.gib')
ORDER BY code;

COMMIT;
