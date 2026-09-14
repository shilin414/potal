package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/automation/scheduler"
	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
)

// scheduleRecord is the wire shape of a Schedule.
// deliveryRecord is the wire shape of one configured target.
type deliveryRecord struct {
	ID          int64  `json:"id"`
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	TargetName  string `json:"target_name"`
	ContentMode string `json:"content_mode"`
	Enabled     bool   `json:"enabled"`
}

type scheduleRecord struct {
	ID                     int64                  `json:"id"`
	Name                   string                 `json:"name"`
	Description            string                 `json:"description"`
	ApplicationID          int64                  `json:"application_id"`
	Prompt                 string                 `json:"prompt"`
	ScheduleType           string                 `json:"schedule_type"`
	CronExpression         string                 `json:"cron_expression"`
	Timezone               string                 `json:"timezone"`
	RunAt                  *string                `json:"run_at"`
	Trigger                schedule.TriggerConfig `json:"trigger"`
	Enabled                bool                   `json:"enabled"`
	ConversationPolicy     string                 `json:"conversation_policy"`
	OverlapPolicy          string                 `json:"overlap_policy"`
	MisfirePolicy          string                 `json:"misfire_policy"`
	DeadlinePolicy         string                 `json:"deadline_policy"`
	ExecutionWindowSeconds int                    `json:"execution_window_seconds"`
	NextRunAt              *string                `json:"next_run_at"`
	LastRunAt              *string                `json:"last_run_at"`
	CreatedAt              string                 `json:"created_at"`
	UpdatedAt              string                 `json:"updated_at"`
	LastOccurrence         *schedule.Occurrence   `json:"last_occurrence,omitempty"`
	Deliveries             []deliveryRecord       `json:"deliveries,omitempty"`
}

func toScheduleRecord(s *schedule.Schedule, last *schedule.Occurrence) scheduleRecord {
	prompt := ""
	if p, ok := s.InputPayload["prompt"].(string); ok {
		prompt = p
	}
	rec := scheduleRecord{
		ID:                     s.ID,
		Name:                   s.Name,
		Description:            s.Description,
		ApplicationID:          s.ApplicationID,
		Prompt:                 prompt,
		ScheduleType:           s.ScheduleType,
		CronExpression:         s.CronExpression,
		Timezone:               s.Timezone,
		Trigger:                s.TriggerConfig,
		Enabled:                s.Enabled,
		ConversationPolicy:     s.ConversationPolicy,
		OverlapPolicy:          s.OverlapPolicy,
		MisfirePolicy:          s.MisfirePolicy,
		DeadlinePolicy:         s.DeadlinePolicy,
		ExecutionWindowSeconds: s.ExecutionWindowSeconds,
		NextRunAt:              isoPtr(s.NextRunAt),
		LastRunAt:              isoPtr(s.LastRunAt),
		CreatedAt:              iso(s.CreatedAt),
		UpdatedAt:              iso(s.UpdatedAt),
		LastOccurrence:         last,
	}
	rec.RunAt = isoPtr(s.RunAt)
	return rec
}

// scheduleUpsertBody mirrors ScheduleUpsertRequest in the contract.
type scheduleUpsertBody struct {
	Name                   *string                 `json:"name"`
	Description            *string                 `json:"description"`
	ApplicationID          *int64                  `json:"application_id"`
	Prompt                 *string                 `json:"prompt"`
	ScheduleType           *string                 `json:"schedule_type"`
	Timezone               *string                 `json:"timezone"`
	RunAt                  *time.Time              `json:"run_at"`
	Trigger                *schedule.TriggerConfig `json:"trigger"`
	ConversationPolicy     *string                 `json:"conversation_policy"`
	OverlapPolicy          *string                 `json:"overlap_policy"`
	MisfirePolicy          *string                 `json:"misfire_policy"`
	DeadlinePolicy         *string                 `json:"deadline_policy"`
	ExecutionWindowSeconds *int                    `json:"execution_window_seconds"`
	Deliveries             *[]deliveryInput        `json:"deliveries"`
}

type deliveryInput struct {
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	TargetName  string `json:"target_name"`
	ContentMode string `json:"content_mode"`
}

func toDomainDeliveries(in []deliveryInput) []schedule.DeliveryInput {
	if len(in) == 0 {
		return nil
	}
	out := make([]schedule.DeliveryInput, 0, len(in))
	for _, d := range in {
		out = append(out, schedule.DeliveryInput(d))
	}
	return out
}

// writeValidation maps domain validation errors to the field-errors shape.
func writeValidation(w http.ResponseWriter, err error) {
	var ve *schedule.ValidationError
	if errors.As(err, &ve) {
		writeFieldErrors(w, map[string][]string{"detail": {ve.Msg}})
		return
	}
	// Non-validation errors are server faults, not client input problems.
	writeSimpleError(w, http.StatusInternalServerError, err.Error())
}

// ───────────────────────────────────────────────────────────── handlers ──

// ListSchedules implements GET /api/v2/schedules.
func (s *Server) ListSchedules(w http.ResponseWriter, r *http.Request, params genapi.ListSchedulesParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	status := "all"
	if params.Status != nil {
		status = string(*params.Status)
	}
	f := schedule.ListFilter{Status: status, Limit: 50}
	if params.BeforeId != nil {
		f.BeforeID = int64(*params.BeforeId)
	}
	if params.Limit != nil && *params.Limit > 0 && *params.Limit <= 100 {
		f.Limit = *params.Limit
	}
	items, err := s.Schedules.List(r.Context(), caller.ID, f)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]scheduleRecord, 0, len(items))
	for _, it := range items {
		out = append(out, toScheduleRecord(it.Schedule, it.LastOccurrence))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateSchedule implements POST /api/v2/schedules.
func (s *Server) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body scheduleUpsertBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeBare(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Name == nil || body.ApplicationID == nil || body.Prompt == nil || body.ScheduleType == nil {
		writeFieldErrors(w, map[string][]string{
			"detail": {"name, application_id, prompt and schedule_type are required"},
		})
		return
	}
	in := &schedule.CreateInput{
		Name:               *body.Name,
		Description:        derefStr(body.Description),
		ApplicationID:      *body.ApplicationID,
		Prompt:             *body.Prompt,
		ScheduleType:       *body.ScheduleType,
		Timezone:           derefStr(body.Timezone),
		RunAt:              body.RunAt,
		TriggerConfig:      derefTrigger(body.Trigger),
		ConversationPolicy: derefStr(body.ConversationPolicy),
		OverlapPolicy:      derefStr(body.OverlapPolicy),
		MisfirePolicy:      derefStr(body.MisfirePolicy),
		DeadlinePolicy:     derefStr(body.DeadlinePolicy),
		Deliveries:         toDomainDeliveries(derefDeliveries(body.Deliveries)),
	}
	if body.ExecutionWindowSeconds != nil {
		in.ExecutionWindowSeconds = *body.ExecutionWindowSeconds
	}
	sch, err := s.Schedules.Create(r.Context(), caller.ID, in)
	if err != nil {
		writeValidation(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toScheduleRecord(sch, nil))
}

// PreviewScheduleRuns implements POST /api/v2/schedules/preview.
func (s *Server) PreviewScheduleRuns(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body scheduleUpsertBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeBare(w, http.StatusBadRequest, "invalid json")
		return
	}
	in := &schedule.CreateInput{
		Name:               derefStr(body.Name),
		ApplicationID:      derefInt64(body.ApplicationID),
		Prompt:             derefStr(body.Prompt),
		ScheduleType:       derefStr(body.ScheduleType),
		Timezone:           derefStr(body.Timezone),
		RunAt:              body.RunAt,
		TriggerConfig:      derefTrigger(body.Trigger),
		ConversationPolicy: derefStr(body.ConversationPolicy),
		OverlapPolicy:      derefStr(body.OverlapPolicy),
		MisfirePolicy:      derefStr(body.MisfirePolicy),
		DeadlinePolicy:     derefStr(body.DeadlinePolicy),
	}
	runs, err := schedule.PreviewRuns(in, 3)
	if err != nil {
		writeValidation(w, err)
		return
	}
	out := make([]string, 0, len(runs))
	for _, t := range runs {
		out = append(out, iso(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"next_runs": out})
}

// GetSchedule implements GET /api/v2/schedules/{id}.
func (s *Server) GetSchedule(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	sch, err := s.Schedules.Get(r.Context(), int64(id), caller.ID, caller.IsStaff)
	if err != nil {
		writeScheduleNotFound(w, err)
		return
	}
	rec := toScheduleRecord(sch, nil)
	dels, err := s.Schedules.ListDeliveries(r.Context(), sch.ID, caller.ID, caller.IsStaff)
	if err != nil {
		s.Log.Error("list schedule deliveries failed", "schedule_id", sch.ID, "err", err)
	} else if len(dels) > 0 {
		dl := make([]deliveryRecord, 0, len(dels))
		for _, d := range dels {
			dl = append(dl, deliveryRecord{
				ID: d.ID, TargetType: d.TargetType, TargetID: d.TargetID,
				TargetName: d.TargetName, ContentMode: d.ContentMode, Enabled: d.Enabled,
			})
		}
		rec.Deliveries = dl
	}
	writeJSON(w, http.StatusOK, rec)
}

// UpdateSchedule implements PATCH /api/v2/schedules/{id}.
func (s *Server) UpdateSchedule(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body scheduleUpsertBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeBare(w, http.StatusBadRequest, "invalid json")
		return
	}
	in := &schedule.UpdateInput{
		Name:                   body.Name,
		Description:            body.Description,
		Prompt:                 body.Prompt,
		ApplicationID:          body.ApplicationID,
		ScheduleType:           body.ScheduleType,
		Timezone:               body.Timezone,
		TriggerConfig:          body.Trigger,
		ConversationPolicy:     body.ConversationPolicy,
		OverlapPolicy:          body.OverlapPolicy,
		MisfirePolicy:          body.MisfirePolicy,
		DeadlinePolicy:         body.DeadlinePolicy,
		ExecutionWindowSeconds: body.ExecutionWindowSeconds,
	}
	if body.RunAt != nil {
		t := *body.RunAt
		ptr := &t
		in.RunAt = &ptr
	}
	if body.Deliveries != nil {
		dl := toDomainDeliveries(*body.Deliveries)
		in.Deliveries = &dl
	}
	sch, err := s.Schedules.Update(r.Context(), int64(id), caller.ID, caller.IsStaff, in)
	if err != nil {
		writeScheduleNotFoundOrValidation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toScheduleRecord(sch, nil))
}

// DeleteSchedule implements DELETE /api/v2/schedules/{id}.
func (s *Server) DeleteSchedule(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if err := s.Schedules.Delete(r.Context(), int64(id), caller.ID, caller.IsStaff); err != nil {
		writeScheduleNotFound(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EnableSchedule implements POST /api/v2/schedules/{id}/enable.
func (s *Server) EnableSchedule(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId) {
	s.setScheduleEnabled(w, r, id, true)
}

// DisableSchedule implements POST /api/v2/schedules/{id}/disable.
func (s *Server) DisableSchedule(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId) {
	s.setScheduleEnabled(w, r, id, false)
}

func (s *Server) setScheduleEnabled(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId, enabled bool) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	sch, err := s.Schedules.SetEnabled(r.Context(), int64(id), caller.ID, caller.IsStaff, enabled)
	if err != nil {
		writeScheduleNotFoundOrValidation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toScheduleRecord(sch, nil))
}

// RunScheduleNow implements POST /api/v2/schedules/{id}/run-now.
func (s *Server) RunScheduleNow(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	occ, err := s.Scheduler.TriggerNow(r.Context(), int64(id), caller.ID, caller.IsStaff)
	if err != nil {
		switch {
		case errors.Is(err, schedule.ErrNotFound):
			writeDetail(w, http.StatusNotFound, "Not found.")
		case errors.Is(err, scheduler.ErrNotSchedulable):
			writeDetail(w, http.StatusConflict, "application has no enabled runtime binding")
		default:
			writeDetail(w, http.StatusConflict, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, occ)
}

// ListScheduleOccurrences implements GET /api/v2/schedules/{id}/occurrences.
func (s *Server) ListScheduleOccurrences(w http.ResponseWriter, r *http.Request, id genapi.ScheduleId, params genapi.ListScheduleOccurrencesParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	limit := 50
	if params.Limit != nil && *params.Limit > 0 && *params.Limit <= 100 {
		limit = *params.Limit
	}
	var before int64
	if params.BeforeId != nil {
		before = int64(*params.BeforeId)
	}
	occs, err := s.Schedules.ListOccurrences(r.Context(), int64(id), caller.ID, caller.IsStaff, before, limit)
	if err != nil {
		writeScheduleNotFound(w, err)
		return
	}
	if occs == nil {
		occs = []*schedule.Occurrence{}
	}
	writeJSON(w, http.StatusOK, occs)
}

// ───────────────────────────────────────────────────────────── helpers ──

func writeScheduleNotFound(w http.ResponseWriter, err error) {
	if errors.Is(err, schedule.ErrNotFound) {
		writeDetail(w, http.StatusNotFound, "Not found.")
		return
	}
	writeSimpleError(w, http.StatusInternalServerError, err.Error())
}

func writeScheduleNotFoundOrValidation(w http.ResponseWriter, err error) {
	if errors.Is(err, schedule.ErrNotFound) {
		writeDetail(w, http.StatusNotFound, "Not found.")
		return
	}
	writeValidation(w, err)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefTrigger(p *schedule.TriggerConfig) schedule.TriggerConfig {
	if p == nil {
		return schedule.TriggerConfig{}
	}
	return *p
}

func derefDeliveries(p *[]deliveryInput) []deliveryInput {
	if p == nil {
		return nil
	}
	return *p
}
