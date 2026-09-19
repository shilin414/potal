package http

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
)

const (
	defaultTaskPageLimit = 20
	maxTaskPageLimit     = 50
)

type taskCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        uint64    `json:"id"`
}

func encodeTaskCursor(updatedAt time.Time, id uint64) string {
	raw, _ := json.Marshal(taskCursor{UpdatedAt: updatedAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeTaskCursor(raw string) (taskCursor, error) {
	var cursor taskCursor
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, err
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursor, err
	}
	if cursor.ID == 0 || cursor.UpdatedAt.IsZero() {
		return cursor, errors.New("invalid task cursor")
	}
	return cursor, nil
}

func taskString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func taskSummaryFromValues(
	id uint64,
	title string,
	applicationID sql.NullInt64,
	createdAt, updatedAt time.Time,
	applicationSlug, applicationName, applicationIcon, applicationColor, applicationKind sql.NullString,
	previewRole, preview, executionState string,
) genapi.TaskSummary {
	out := genapi.TaskSummary{
		Id:             strconv.FormatUint(id, 10),
		Title:          title,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		ExecutionState: genapi.TaskSummaryExecutionState(executionState),
	}
	if applicationID.Valid {
		value := applicationID.Int64
		out.ApplicationId = &value
	}
	if applicationSlug.Valid {
		value := applicationSlug.String
		out.ApplicationSlug = &value
	}
	if applicationName.Valid {
		value := applicationName.String
		out.ApplicationName = &value
	}
	if applicationIcon.Valid {
		value := applicationIcon.String
		out.ApplicationIcon = &value
	}
	if applicationColor.Valid {
		value := applicationColor.String
		out.ApplicationColor = &value
	}
	if applicationKind.Valid {
		value := applicationKind.String
		out.ApplicationKind = &value
	}
	if previewRole != "" {
		value := previewRole
		out.PreviewRole = &value
	}
	if preview != "" {
		value := preview
		out.Preview = &value
	}
	return out
}

func taskSummaryFromListRow(row db.ListTasksByUserPageRow) genapi.TaskSummary {
	return taskSummaryFromValues(
		row.ID, row.Title, row.ApplicationID, row.CreatedAt, row.UpdatedAt,
		row.ApplicationSlug, row.ApplicationName, row.ApplicationIcon,
		row.ApplicationColor, row.ApplicationKind, taskString(row.PreviewRole), taskString(row.Preview),
		row.ExecutionState,
	)
}

func taskSummaryFromDetailRow(row db.GetTaskByIDOwnedRow) genapi.TaskSummary {
	return taskSummaryFromValues(
		row.ID, row.Title, row.ApplicationID, row.CreatedAt, row.UpdatedAt,
		row.ApplicationSlug, row.ApplicationName, row.ApplicationIcon,
		row.ApplicationColor, row.ApplicationKind, taskString(row.PreviewRole), taskString(row.Preview),
		row.ExecutionState,
	)
}

// ListTasks implements GET /api/v2/tasks. Conversation remains the storage
// model; this handler is the product-facing anti-corruption layer.
func (s *Server) ListTasks(w http.ResponseWriter, r *http.Request, params genapi.ListTasksParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}

	limit := defaultTaskPageLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > maxTaskPageLimit {
		writeDetail(w, http.StatusBadRequest, "limit must be between 1 and 50")
		return
	}

	var cursorUpdatedAt sql.NullTime
	var cursorID uint64
	if params.Cursor != nil && strings.TrimSpace(*params.Cursor) != "" {
		cursor, err := decodeTaskCursor(strings.TrimSpace(*params.Cursor))
		if err != nil {
			writeDetail(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cursorUpdatedAt = sql.NullTime{Time: cursor.UpdatedAt, Valid: true}
		cursorID = cursor.ID
	}

	var applicationID sql.NullInt64
	if params.ApplicationId != nil {
		if *params.ApplicationId <= 0 {
			writeDetail(w, http.StatusBadRequest, "application_id must be positive")
			return
		}
		applicationID = sql.NullInt64{Int64: *params.ApplicationId, Valid: true}
	}

	var search any
	var searchLike any
	if params.Q != nil {
		value := strings.TrimSpace(*params.Q)
		if utf8.RuneCountInString(value) > 200 {
			writeDetail(w, http.StatusBadRequest, "q must be at most 200 characters")
			return
		}
		if value != "" {
			search = value
			searchLike = "%" + value + "%"
		}
	}

	rows, err := s.Runs.Querier().ListTasksByUserPage(r.Context(), db.ListTasksByUserPageParams{
		UserID:          uint64(caller.ID),
		ApplicationID:   applicationID,
		Search:          search,
		SearchLike:      searchLike,
		CursorUpdatedAt: cursorUpdatedAt,
		CursorID:        cursorID,
		Limit:           int32(limit + 1),
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]genapi.TaskSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, taskSummaryFromListRow(row))
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = encodeTaskCursor(last.UpdatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, genapi.TaskPage{Items: items, NextCursor: nextCursor})
}

// UpdateTask implements PATCH /api/v2/tasks/{taskId}.
func (s *Server) UpdateTask(w http.ResponseWriter, r *http.Request, taskID int64) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if taskID <= 0 {
		writeDetail(w, http.StatusNotFound, "task not found")
		return
	}
	var body genapi.TaskUpdateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeDetail(w, http.StatusBadRequest, "invalid request body")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || utf8.RuneCountInString(title) > 200 {
		writeDetail(w, http.StatusBadRequest, "title must contain 1 to 200 characters")
		return
	}
	result, err := s.Runs.Querier().RenameTaskOwned(r.Context(), db.RenameTaskOwnedParams{
		Title: title, ID: uint64(taskID), UserID: uint64(caller.ID),
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if affected == 0 {
		writeDetail(w, http.StatusNotFound, "task not found")
		return
	}
	row, err := s.Runs.Querier().GetTaskByIDOwned(r.Context(), db.GetTaskByIDOwnedParams{
		ID: uint64(taskID), UserID: uint64(caller.ID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeDetail(w, http.StatusNotFound, "task not found")
			return
		}
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, taskSummaryFromDetailRow(row))
}

// DeleteTask implements DELETE /api/v2/tasks/{taskId} by delegating to the
// existing conversation deletion service and its safety fences.
func (s *Server) DeleteTask(w http.ResponseWriter, r *http.Request, taskID int64) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if taskID <= 0 {
		writeDetail(w, http.StatusNotFound, "task not found")
		return
	}
	err := s.Runs.DeleteConversationCascade(r.Context(), taskID, caller.ID)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, execution.ErrConversationNotFound):
		writeDetail(w, http.StatusNotFound, "task not found")
	case errors.Is(err, execution.ErrConversationHasActiveRun):
		writeDetail(w, http.StatusConflict, "task has an active execution")
	case errors.Is(err, execution.ErrConversationHasScheduledRuns):
		writeDetail(w, http.StatusConflict, "scheduled tasks cannot be deleted while automation history or deliveries still reference them")
	default:
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
	}
}
