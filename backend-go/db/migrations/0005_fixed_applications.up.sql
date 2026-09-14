-- Fixed applications (应用中心) — enable flag + seeded business apps.
--
-- `enabled` is the 应用中心 switch (架构文档 §21): a disabled application is
-- hidden from every non-admin surface (home shortcuts / switcher / app
-- center) while admins still see it for management. Chat agents keep their
-- runtime binding as the primary switch; enabled defaults to 1 so existing
-- rows are unaffected.
-- NOTE: the index is created in a statement separate from the column it
-- covers, so the migration never depends on same-statement column
-- visibility ordering.
ALTER TABLE applications
    ADD COLUMN enabled TINYINT(1) NOT NULL DEFAULT 1 AFTER is_default_agent;

ALTER TABLE applications
    ADD KEY idx_applications_kind_enabled (kind, enabled);

-- Category for the seeded fixed apps. INSERT IGNORE: slug or name may
-- already exist from a previous environment, in which case the SELECT below
-- still resolves whichever row carries the slug.
INSERT IGNORE INTO application_categories (slug, name, description, icon, sort_order)
VALUES ('apps', '应用', '企业固定业务应用', '🧩', 40);

-- Fixed applications are code-deployed (frontend renderer + backend API,
-- 架构文档 §70/§71), so they are seeded here instead of authored in the
-- market. Their pages are rendered by the frontend renderer registry keyed
-- on renderer_key; business logic lands later.
INSERT IGNORE INTO applications
    (slug, name, description, icon, color, kind, renderer_key,
     category_id, is_public, is_default_agent, enabled, usage_count,
     created_by, organization_id)
SELECT 'barcode-query', '条码信息查询', '按条码查询商品档案与流向信息', '🔍', '#2563eb',
       'page', 'barcode-query',
       (SELECT id FROM application_categories WHERE slug = 'apps'),
       1, 0, 1, 0, NULL, NULL
UNION ALL
SELECT 'oa-unlock', 'OA账号解锁', 'OA 账号被锁定时自助提交解锁申请', '🔓', '#7c3aed',
       'form', 'oa-unlock',
       (SELECT id FROM application_categories WHERE slug = 'apps'),
       1, 0, 1, 0, NULL, NULL
UNION ALL
SELECT 'oa-password', '修改OA密码', '自助修改 OA 登录密码', '🔑', '#d97706',
       'form', 'oa-password',
       (SELECT id FROM application_categories WHERE slug = 'apps'),
       1, 0, 1, 0, NULL, NULL
UNION ALL
SELECT 'material-query', '物料信息查询', '按编码或名称查询物料主数据', '📦', '#059669',
       'page', 'material-query',
       (SELECT id FROM application_categories WHERE slug = 'apps'),
       1, 0, 1, 0, NULL, NULL;
