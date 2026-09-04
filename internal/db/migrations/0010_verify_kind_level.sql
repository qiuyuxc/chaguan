-- 认证引入分类(官方/厂商/作者):verify_title 降级为「显示文案」,
-- verify_kind 决定 V 的颜色;旧数据按称号映射分类。
ALTER TABLE users ADD COLUMN verify_kind TEXT;
ALTER TABLE users ADD COLUMN level_override INTEGER;

UPDATE users SET verify_kind = '官方' WHERE verify_title = '官号';
UPDATE users SET verify_kind = '作者' WHERE verify_title = '认证作者';
UPDATE verify_requests SET kind = '官方' WHERE kind = '官号';
UPDATE verify_requests SET kind = '作者' WHERE kind = '认证作者';
