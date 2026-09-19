package http

import (
	"database/sql"
	"testing"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
)

func TestTaskCursorRoundTrip(t *testing.T) {
	updated := time.Date(2026, 9, 19, 8, 30, 12, 123000000, time.UTC)
	encoded := encodeTaskCursor(updated, 42)
	decoded, err := decodeTaskCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if decoded.ID != 42 || !decoded.UpdatedAt.Equal(updated) {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
}

func TestTaskSummaryAdapterKeepsTaskSemantics(t *testing.T) {
	now := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	row := db.ListTasksByUserPageRow{
		ID: 7, Title: "销售分析", ApplicationID: sql.NullInt64{Int64: 3, Valid: true},
		CreatedAt: now, UpdatedAt: now, ApplicationSlug: sql.NullString{String: "sales", Valid: true},
		ApplicationName: sql.NullString{String: "销售助手", Valid: true}, Preview: "继续分析",
		PreviewRole: "user", ExecutionState: "running",
	}
	got := taskSummaryFromListRow(row)
	if got.Id != "7" || got.ApplicationId == nil || *got.ApplicationId != 3 {
		t.Fatalf("unexpected task: %#v", got)
	}
	if got.ExecutionState != "running" || got.Preview == nil || *got.Preview != "继续分析" {
		t.Fatalf("unexpected execution/preview: %#v", got)
	}
}
