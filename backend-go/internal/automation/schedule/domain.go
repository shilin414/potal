package schedule

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
)

// runIDString renders a BINARY(16) run id the same way ids.ID.String does
// (canonical UUID form used across the API).
func runIDString(b string) string { return uuid.UUID([]byte(b)).String() }

// Occurrence / delivery statuses shared across the automation packages.
const (
	OccPending   = "pending"
	OccQueued    = "queued"
	OccRunning   = "running"
	OccSucceeded = "succeeded"
	OccFailed    = "failed"
	OccSkipped   = "skipped"

	DeliveryPending   = "pending"
	DeliverySending   = "sending"
	DeliverySucceeded = "succeeded"
	DeliveryFailed    = "failed"
	DeliverySkipped   = "skipped"
)

// Run priorities (queue fairness: interactive beats scheduled).
const (
	PriorityInteractiveUser = "interactive_user"
	PriorityScheduledNormal = "scheduled_normal"
	PriorityScheduledHigh   = "scheduled_high"
)

// TriggerTypeScheduled marks runs created by the scheduler.
const TriggerTypeScheduled = "scheduled"

// Schedule is the domain view of a schedules row.
type Schedule struct {
	ID                     int64
	OwnerUserID            int64
	Name                   string
	Description            string
	ApplicationID          int64
	InputPayload           map[string]any
	ScheduleType           string
	CronExpression         string
	TriggerConfig          TriggerConfig
	Timezone               string
	RunAt                  *time.Time
	Enabled                bool
	ConversationPolicy     string
	ConversationID         *int64
	OverlapPolicy          string
	MisfirePolicy          string
	ExecutionWindowSeconds int
	DeadlinePolicy         string
	NextRunAt              *time.Time
	LastRunAt              *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Delivery is one Feishu send target attached to a schedule.
type Delivery struct {
	ID                 int64  `json:"id"`
	ScheduleID         int64  `json:"schedule_id"`
	Channel            string `json:"channel"`
	SenderIdentityMode string `json:"sender_identity_mode"`
	TargetType         string `json:"target_type"` // user | chat
	TargetID           string `json:"target_id"`
	TargetName         string `json:"target_name"`
	ContentMode        string `json:"content_mode"`
	Enabled            bool   `json:"enabled"`
}

// DeliveryExecution is the per-target send state of one occurrence
// (independent of the Run's AI outcome — Run ≠ Delivery).
type DeliveryExecution struct {
	ID                 string     `json:"id"`
	OccurrenceID       int64      `json:"occurrence_id"`
	ScheduleDeliveryID int64      `json:"schedule_delivery_id"`
	TargetType         string     `json:"target_type"`
	TargetID           string     `json:"target_id"`
	Status             string     `json:"status"`
	Attempt            int        `json:"attempt"`
	ErrorCode          string     `json:"error_code,omitempty"`
	ErrorMessage       string     `json:"error_message,omitempty"`
	SentAt             *time.Time `json:"sent_at"`
}

// Occurrence is one scheduled time slot of a schedule.
type Occurrence struct {
	ID          int64               `json:"id"`
	ScheduleID  int64               `json:"schedule_id"`
	ScheduledAt time.Time           `json:"scheduled_at"`
	EnqueuedAt  *time.Time          `json:"enqueued_at"`
	AdmittedAt  *time.Time          `json:"admitted_at"`
	RunID       string              `json:"run_id,omitempty"`
	Status      string              `json:"status"`
	TriggeredAt *time.Time          `json:"triggered_at"`
	FinishedAt  *time.Time          `json:"finished_at"`
	CreatedAt   time.Time           `json:"created_at"`
	Deliveries  []DeliveryExecution `json:"deliveries,omitempty"`
}

func fromDBRow(r db.Schedule) *Schedule {
	out := &Schedule{
		ID:                     int64(r.ID),
		OwnerUserID:            int64(r.OwnerUserID),
		Name:                   r.Name,
		ApplicationID:          int64(r.ApplicationID),
		ScheduleType:           r.ScheduleType,
		CronExpression:         r.CronExpression,
		Timezone:               r.Timezone,
		Enabled:                r.Enabled,
		ConversationPolicy:     r.ConversationPolicy,
		OverlapPolicy:          r.OverlapPolicy,
		MisfirePolicy:          r.MisfirePolicy,
		ExecutionWindowSeconds: int(r.ExecutionWindowSeconds),
		DeadlinePolicy:         r.DeadlinePolicy,
		CreatedAt:              r.CreatedAt,
		UpdatedAt:              r.UpdatedAt,
	}
	if r.Description.Valid {
		out.Description = r.Description.String
	}
	_ = json.Unmarshal(r.InputPayload, &out.InputPayload)
	_ = json.Unmarshal(r.TriggerConfig, &out.TriggerConfig)
	if out.InputPayload == nil {
		out.InputPayload = map[string]any{}
	}
	if r.RunAt.Valid {
		t := r.RunAt.Time
		out.RunAt = &t
	}
	if r.ConversationID.Valid {
		v := int64(r.ConversationID.Int64)
		out.ConversationID = &v
	}
	if r.NextRunAt.Valid {
		t := r.NextRunAt.Time
		out.NextRunAt = &t
	}
	if r.LastRunAt.Valid {
		t := r.LastRunAt.Time
		out.LastRunAt = &t
	}
	return out
}

func FromDBRow(r db.Schedule) *Schedule { return fromDBRow(r) }

func occurrenceFromDBRow(r db.ScheduleOccurrence) *Occurrence {
	return OccurrenceFromDBRow(r)
}

// OccurrenceFromDBRow exposes the gen/db row → domain conversion.
func OccurrenceFromDBRow(r db.ScheduleOccurrence) *Occurrence {
	out := &Occurrence{
		ID:          int64(r.ID),
		ScheduleID:  int64(r.ScheduleID),
		ScheduledAt: r.ScheduledAt,
		Status:      r.Status,
		CreatedAt:   r.CreatedAt,
	}
	if r.EnqueuedAt.Valid {
		t := r.EnqueuedAt.Time
		out.EnqueuedAt = &t
	}
	if r.AdmittedAt.Valid {
		t := r.AdmittedAt.Time
		out.AdmittedAt = &t
	}
	if len(r.RunID.String) == 16 {
		out.RunID = runIDString(r.RunID.String)
	}
	if r.TriggeredAt.Valid {
		t := r.TriggeredAt.Time
		out.TriggeredAt = &t
	}
	if r.FinishedAt.Valid {
		t := r.FinishedAt.Time
		out.FinishedAt = &t
	}
	return out
}

// DeliveryExecutionFromDBRow converts a delivery row for display.
func DeliveryExecutionFromDBRow(r db.DeliveryExecution) DeliveryExecution {
	out := DeliveryExecution{
		ID:                 runIDString(string(r.ID)),
		OccurrenceID:       int64(r.OccurrenceID),
		ScheduleDeliveryID: int64(r.ScheduleDeliveryID),
		TargetType:         r.TargetType,
		TargetID:           r.TargetID,
		Status:             r.Status,
		Attempt:            int(r.Attempt),
		ErrorCode:          r.ErrorCode,
	}
	if r.ErrorMessage.Valid {
		out.ErrorMessage = r.ErrorMessage.String
	}
	if r.SentAt.Valid {
		t := r.SentAt.Time
		out.SentAt = &t
	}
	return out
}
