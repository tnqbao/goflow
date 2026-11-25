package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tnqbao/kubereel/internal/scheduler"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const scheduleInterval = 10 * time.Second
const scheduledSource = "kubereel.io/scheduled-workflow-uid"
const scheduledTime = "kubereel.io/scheduled-at"

func (s *Schedules) Run(ctx context.Context) {
	ticker := time.NewTicker(scheduleInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := s.Reconcile(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.Error("reconcile workflow schedules", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Schedules) Reconcile(ctx context.Context, now time.Time) error {
	cursor := ""
	for {
		page, err := s.definitions.List(ctx, 100, cursor)
		if err != nil {
			return err
		}
		for i := range page.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := &page.Items[i]
			if _, scheduled := item.GetAnnotations()[scheduleAnnotation]; !scheduled {
				continue
			}
			if err := s.reconcileSchedule(ctx, item, now); err != nil && !apierrors.IsConflict(err) && ctx.Err() == nil {
				slog.Warn("workflow schedule not submitted", "workflow", item.GetName(), "error", err)
			}
		}
		cursor = page.GetContinue()
		if cursor == "" {
			return nil
		}
	}
}

func (s *Schedules) reconcileSchedule(ctx context.Context, item *unstructured.Unstructured, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if item.GetDeletionTimestamp() != nil {
		return nil
	}
	config, err := readSchedule(item)
	if err != nil {
		return err
	}
	state, err := readScheduleState(item)
	if err != nil {
		return err
	}
	rule, err := scheduler.Parse(config.Cron)
	if err != nil {
		return err
	}
	if state.PendingAt != nil {
		return s.recoverOccurrence(ctx, item, config, state, now)
	}
	if config.Paused {
		return nil
	}
	after := config.UpdatedAt
	if state.EvaluatedAt != nil && state.EvaluatedAt.After(after) {
		after = *state.EvaluatedAt
	}
	due := scheduler.LatestDue(rule, after, now)
	if due.IsZero() {
		return nil
	}
	active, err := s.previousRunActive(ctx, state.LastRunID)
	if err != nil {
		return err
	}
	state.EvaluatedAt = &due
	if active {
		_, err = s.saveScheduleState(ctx, item, state)
		return err
	}
	state.PendingAt = &due
	reserved, err := s.saveScheduleState(ctx, item, state)
	if err != nil {
		return err
	}
	return s.recoverOccurrence(ctx, reserved, config, state, now)
}

func (s *Schedules) recoverOccurrence(ctx context.Context, item *unstructured.Unstructured, config workflowSchedule, state scheduleState, now time.Time) error {
	due := *state.PendingAt
	uid := string(item.GetUID())
	if uid == "" {
		return fmt.Errorf("scheduled workflow is missing its UID")
	}
	tick := due.UTC().Format(time.RFC3339)
	digest := sha256.Sum256([]byte(uid + "\x00" + tick))
	name := fmt.Sprintf("scheduled-%x", digest[:16])
	run, err := s.runs.Get(ctx, name)
	switch {
	case err == nil:
		return s.finishOccurrence(ctx, item, run, state, uid, tick)
	case !apierrors.IsNotFound(err):
		return err
	case config.Paused || !due.After(config.UpdatedAt) || now.Sub(due) > scheduler.CatchUpWindow:
		state.PendingAt = nil
		_, err = s.saveScheduleState(ctx, item, state)
		return err
	}
	spec, err := executionSpec(item, config.ExecutionOptions)
	if err != nil {
		state.PendingAt = nil
		_, saveErr := s.saveScheduleState(ctx, item, state)
		return errors.Join(fmt.Errorf("scheduled run configuration is invalid: %w", err), saveErr)
	}
	run, err = s.runs.Create(ctx, name, map[string]string{scheduledSource: uid, scheduledTime: tick}, spec)
	if apierrors.IsAlreadyExists(err) {
		run, err = s.runs.Get(ctx, name)
	}
	if err != nil {
		return err
	}
	return s.finishOccurrence(ctx, item, run, state, uid, tick)
}

func (s *Schedules) finishOccurrence(ctx context.Context, item, run *unstructured.Unstructured, state scheduleState, uid, tick string) error {
	annotations := run.GetAnnotations()
	if annotations[scheduledSource] != uid || annotations[scheduledTime] != tick {
		return fmt.Errorf("scheduled run name is occupied by a different resource")
	}
	state.LastRunID = run.GetName()
	state.LastRunAt = state.PendingAt
	state.PendingAt = nil
	_, err := s.saveScheduleState(ctx, item, state)
	return err
}

func (s *Schedules) saveScheduleState(ctx context.Context, item *unstructured.Unstructured, state scheduleState) (*unstructured.Unstructured, error) {
	updated := item.DeepCopy()
	if err := setScheduleAnnotation(updated, scheduleStateAnnotation, state); err != nil {
		return nil, err
	}
	return s.definitions.Update(ctx, updated)
}

func (s *Schedules) previousRunActive(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	run, err := s.runs.Get(ctx, id)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	view := executionView(run)
	if view.Phase == "Succeeded" {
		return false, nil
	}
	if !canRetryExecution(view) {
		return true, nil
	}
	return s.runs.HasActiveJobs(ctx, run)
}
