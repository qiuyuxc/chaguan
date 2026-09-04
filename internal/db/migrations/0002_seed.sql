-- 默认版块:保证全新安装后首页不是空的
INSERT INTO categories (slug, name, description, position)
VALUES ('general', '综合', '万事皆可聊', 0);
