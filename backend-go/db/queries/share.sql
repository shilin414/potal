-- ────────────────────────────────────────────────── conversation shares ──

-- name: CreateConversationShare :execresult
INSERT INTO conversation_shares (token, conversation_id, user_id, snapshot)
VALUES (?, ?, ?, ?);

-- name: GetConversationShareByToken :one
SELECT id, token, conversation_id, user_id, snapshot, created_at, revoked_at
FROM conversation_shares WHERE token = ?;

-- name: RevokeConversationShare :exec
UPDATE conversation_shares SET revoked_at = CURRENT_TIMESTAMP(3)
WHERE token = ? AND user_id = ?;

-- name: DeleteSharesByConversation :exec
DELETE FROM conversation_shares WHERE conversation_id = ?;
