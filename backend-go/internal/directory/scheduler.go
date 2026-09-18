package directory

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

type Scheduler struct {
	Repo          *Repo
	Sync          *Service
	Log           *slog.Logger
	Owner         string
	PollInterval  time.Duration
	LeaseDuration time.Duration
}

func NewScheduler(repo *Repo, syncer *Service, owner string, log *slog.Logger) *Scheduler {
	if owner == "" {
		owner = "directory-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	owner = owner + ":" + ids.New().String()
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{Repo: repo, Sync: syncer, Log: log, Owner: owner, PollInterval: 15 * time.Second, LeaseDuration: 10 * time.Minute}
}
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.PollInterval)
	defer ticker.Stop()
	for {
		if err := s.tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("directory sync tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Scheduler) tick(ctx context.Context) error {
	ok, err := s.Repo.AcquireLease(ctx, s.Owner, s.LeaseDuration)
	if err != nil || !ok {
		return err
	}
	defer s.Repo.ReleaseLease(context.Background(), s.Owner)
	if err := s.Repo.RecoverStaleRuns(ctx); err != nil {
		return err
	}
	cfg, err := s.Repo.GetSyncConfig(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if cfg.Enabled && (cfg.NextRunAt == nil || !cfg.NextRunAt.After(now)) {
		next, err := NextRunAt(*cfg, now)
		if err != nil {
			return err
		}
		if _, err = s.Repo.CreateSyncRun(ctx, "scheduled", nil); err != nil {
			return err
		}
		if err = s.Repo.MarkScheduleStarted(ctx, next); err != nil {
			return err
		}
	}
	run, err := s.Repo.ClaimPendingRun(ctx)
	if err != nil || run == nil {
		return err
	}
	syncCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.renew(syncCtx, cancel)
	if err = s.Sync.RunWithLease(syncCtx, run, s.Owner); err != nil {
		s.Log.Error("directory sync failed", "run_id", run.ID, "err", err)
		return nil
	}
	s.Log.Info("directory sync completed", "run_id", run.ID)
	return nil
}
func (s *Scheduler) renew(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(s.LeaseDuration / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Repo.RenewLease(ctx, s.Owner, s.LeaseDuration); err != nil {
				s.Log.Error("directory lease renew failed; cancelling sync", "err", err)
				cancel()
				return
			}
		}
	}
}
func NextRunAt(cfg SyncConfig, from time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	local := from.In(loc)
	if cfg.ScheduleType == "daily" {
		parts := strings.Split(cfg.DailyTime, ":")
		if len(parts) != 2 {
			return time.Time{}, fmt.Errorf("invalid daily_time")
		}
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		next := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, loc)
		if !next.After(local) {
			next = next.AddDate(0, 0, 1)
		}
		return next.UTC(), nil
	}
	minutes := cfg.IntervalMinutes
	if minutes < 15 {
		minutes = 360
	}
	return from.Add(time.Duration(minutes) * time.Minute).UTC(), nil
}
