-- Seed the Feishu Aily provider (idempotent by provider_key).
-- Capabilities mirror the official Aily custom-agent OpenAPI:
-- streaming/async/conversation/attachment/artifact/visibility = true;
-- cancel/resume = false (no cancel endpoint exists).

INSERT INTO providers
    (provider_key, name, description, supported_runtime_types, capabilities,
     start_rate_limit, max_inflight, poll_rate_limit, artifact_rate_limit,
     timeout_seconds, base_url, status)
SELECT 'feishu_aily', '飞书 Aily', '飞书 Aily 自定义智能体 / 工作流运行时',
       '["agent","workflow"]',
       '{"streaming":true,"async_execution":true,"conversation":true,"attachment":true,"artifact":true,"visibility":true,"cancel":false,"resume":false,"file_upload":true,"human_input":false}',
       '10/s', 100, '10/s', '50/s', 300,
       'https://open.feishu.cn/open-apis', 'active'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM providers WHERE provider_key = 'feishu_aily');
