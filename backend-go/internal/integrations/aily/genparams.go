package aily

import (
	"database/sql"
	"encoding/json"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

func nstr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func dbCreateThreadParams(id []byte, conversationID uint64, provider, authMode, subject string) db.CreateAgentThreadParams {
	return db.CreateAgentThreadParams{
		ID:             id,
		ConversationID: conversationID,
		Provider:       provider,
		AuthMode:       authMode,
		AuthSubjectKey: subject,
	}
}

func dbBindThreadParams(sessionID string, id []byte) db.BindAgentThreadSessionParams {
	return db.BindAgentThreadSessionParams{RemoteID: sessionID, ID: id}
}

func dbUpdateExternalParams(externalID string, runID []byte) db.UpdateRunExternalIDParams {
	return db.UpdateRunExternalIDParams{ExternalRunID: externalID, ID: runID}
}

func dbCreateMessageParams(conversationID uint64, role, content string, metadata json.RawMessage) db.CreateMessageParams {
	return db.CreateMessageParams{
		ConversationID: conversationID,
		Role:           role,
		Content:        content,
		Metadata:       dbtypes.JSONText(metadata),
	}
}

func dbUpsertArtifactParams(id, runID []byte, provider, externalID, providerType, name, normalizedType string) db.UpsertRunArtifactParams {
	return db.UpsertRunArtifactParams{
		ID:                   id,
		RunID:                runID,
		Provider:             provider,
		ExternalArtifactID:   externalID,
		ProviderArtifactType: providerType,
		Name:                 name,
		NormalizedType:       normalizedType,
	}
}

func dbArtifactLookupParams(runID []byte, externalID string) db.ListRunArtifactsByExternalIDParams {
	return db.ListRunArtifactsByExternalIDParams{RunID: runID, ExternalArtifactID: externalID}
}
