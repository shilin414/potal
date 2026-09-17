package http

import (
	"database/sql"
	"encoding/json"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

func nstr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nint(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v > 0} }

func ntime(t time.Time) sql.NullTime { return sql.NullTime{Time: t, Valid: !t.IsZero()} }

func genCreateRunCommandParams(id, runID []byte, cmdType string, payload json.RawMessage, createdBy int64) db.CreateRunCommandParams {
	return db.CreateRunCommandParams{
		ID:          id,
		RunID:       runID,
		CommandType: cmdType,
		Payload:     dbtypes.JSONText(payload),
		CreatedBy:   nint(createdBy),
	}
}

func genCacheArtifactParams(url string, expires time.Time, name, ifParam string, id []byte) db.CacheArtifactURLParams {
	return db.CacheArtifactURLParams{
		CachedExternalUrl:  nstr(url),
		CachedUrlExpiresAt: ntime(expires),
		Column3:            name,
		IF:                 ifParam,
		ID:                 id,
	}
}

func genCreateAttachmentParams(id []byte, provider, attachmentType, name, docURL, storageKey, contentType string, size int64, authMode, authSubject string, createdBy int64) db.CreateAttachmentParams {
	return db.CreateAttachmentParams{
		ID:                   id,
		Provider:             provider,
		ExternalAttachmentID: "",
		AttachmentType:       attachmentType,
		Name:                 name,
		SourceType:           sourceType(docURL),
		SourceUrl:            docURL,
		StorageKey:           storageKey,
		ContentType:          contentType,
		SizeBytes:            uint64(size),
		AuthMode:             authMode,
		AuthSubjectKey:       authSubject,
		Status:               "pending",
		CreatedBy:            nint(createdBy),
	}
}

func genCreateAttachmentParamsExt(id, runID []byte, provider, attachmentType, name, docURL, storageKey, contentType string, size int64, authMode, authSubject string, createdBy int64, externalID string) db.CreateAttachmentParams {
	out := genCreateAttachmentParams(id, provider, attachmentType, name, docURL, storageKey, contentType, size, authMode, authSubject, createdBy)
	// run_id is BINARY(16): bind the raw 16 bytes. (The previous
	// nstr(uuid.String()) wrapped the 36-char text form, which strict-mode
	// MySQL would reject with 1406 Data too long — a gen/db hand-sync drift
	// the sqlc regeneration surfaced; see the sqlc.yaml []byte override.)
	out.RunID = runID
	out.ExternalAttachmentID = externalID
	out.Status = "uploaded"
	return out
}

func genBindAttachmentParams(attID, runID []byte, conversationID uint64) db.BindAttachmentToRunParams {
	return db.BindAttachmentToRunParams{
		RunID:          runID,
		ConversationID: nint(int64(conversationID)),
		ID:             attID,
	}
}

func sourceType(docURL string) string {
	if docURL != "" {
		return "doc_url"
	}
	return "upload"
}
