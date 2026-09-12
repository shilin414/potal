"""Capabilities declared by providers, per the architecture doc.

Aily custom agent (official local docs, 自定义智能体):
    streaming       = true   (SSE, max ~5 minutes)
    async_execution = true   (agent_chat_id + poll get result)
    conversation    = true   (session_id multi-turn)
    attachment      = true   (image / file / feishu_doc / bitable, max 8 ids)
    artifact        = true   (agent_artifact_id, URL valid 24h)
    visibility      = true   (web_sdk channel check, UAT only)
    cancel          = false  (no cancel API in docs)
    resume          = false
"""

AILY_AGENT_CAPABILITIES = {
    'streaming': True,
    'async_execution': True,
    'conversation': True,
    'attachment': True,
    'artifact': True,
    'visibility': True,
    'cancel': False,
    'resume': False,
    'human_input': False,
}
