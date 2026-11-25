package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/scheduler"
	"github.com/tnqbao/kubereel/internal/workflow"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kjson "sigs.k8s.io/json"
)

const scheduleAnnotation = "kubereel.io/schedule"
const scheduleStateAnnotation = "kubereel.io/schedule-state"

var ErrScheduleNotFound = errors.New("workflow schedule not found")
var ErrSchedulesUnavailable = errors.New("workflow schedules unavailable")

type ScheduleOptions struct {
	Cron   string `json:"cron"`
	Paused bool   `json:"paused,omitempty"`
	ExecutionOptions
}

type workflowSchedule struct {
	ScheduleOptions
	UpdatedAt time.Time `json:"updatedAt"`
}

type scheduleState struct {
	EvaluatedAt *time.Time `json:"evaluatedAt,omitempty"`
	PendingAt   *time.Time `json:"pendingAt,omitempty"`
	LastRunAt   *time.Time `json:"lastRunAt,omitempty"`
	LastRunID   string     `json:"lastRunId,omitempty"`
}

type ScheduleView struct {
	workflowSchedule
	WorkflowID string     `json:"workflowId"`
	LastRunAt  *time.Time `json:"lastRunAt,omitempty"`
	LastRunID  string     `json:"lastRunId,omitempty"`
}

type Schedules struct {
	definitions *repository.WorkflowDefinitionRepository
	runs        *repository.ExecutionRepository
}

func NewSchedules(definitions *repository.WorkflowDefinitionRepository, runs *repository.ExecutionRepository) *Schedules {
	return &Schedules{definitions: definitions, runs: runs}
}

func (s *Schedules) Get(ctx context.Context, id string) (ScheduleView, error) {
	item, err := s.lookup(ctx, id)
	if err != nil {
		return ScheduleView{}, err
	}
	schedule, err := readSchedule(item)
	if err != nil {
		return ScheduleView{}, err
	}
	state, err := readScheduleState(item)
	if err != nil {
		return ScheduleView{}, err
	}
	return scheduleView(id, schedule, state), nil
}

func (s *Schedules) Put(ctx context.Context, id string, opts ScheduleOptions) (ScheduleView, error) {
	item, err := s.lookup(ctx, id)
	if err != nil {
		return ScheduleView{}, err
	}
	if _, err := scheduler.Parse(opts.Cron); err != nil {
		return ScheduleView{}, fmt.Errorf("%w: %s", workflow.ErrInvalid, err)
	}
	state, err := readScheduleState(item)
	if err != nil {
		return ScheduleView{}, err
	}
	current, err := readSchedule(item)
	if err == nil && reflect.DeepEqual(current.ScheduleOptions, opts) {
		return scheduleView(id, current, state), nil
	}
	spec, err := executionSpec(item, opts.ExecutionOptions)
	if err != nil {
		return ScheduleView{}, err
	}
	if err := s.runs.ValidateSpec(ctx, spec); err != nil {
		return ScheduleView{}, err
	}
	schedule := workflowSchedule{ScheduleOptions: opts, UpdatedAt: time.Now().UTC()}
	if err := setScheduleAnnotation(item, scheduleAnnotation, schedule); err != nil {
		return ScheduleView{}, err
	}
	if _, err := s.definitions.Update(ctx, item); err != nil {
		return ScheduleView{}, err
	}
	return scheduleView(id, schedule, state), nil
}

func (s *Schedules) Delete(ctx context.Context, id string) error {
	item, err := s.lookup(ctx, id)
	if err != nil {
		return err
	}
	annotations := item.GetAnnotations()
	if _, exists := annotations[scheduleAnnotation]; !exists {
		return ErrScheduleNotFound
	}
	delete(annotations, scheduleAnnotation)
	item.SetAnnotations(annotations)
	_, err = s.definitions.Update(ctx, item)
	return err
}

func (s *Schedules) lookup(ctx context.Context, id string) (*unstructured.Unstructured, error) {
	if s == nil || s.definitions == nil || s.runs == nil {
		return nil, ErrSchedulesUnavailable
	}
	if err := validateWorkflowID(id); err != nil {
		return nil, err
	}
	return s.definitions.Get(ctx, id)
}

func scheduleView(id string, schedule workflowSchedule, state scheduleState) ScheduleView {
	return ScheduleView{workflowSchedule: schedule, WorkflowID: id, LastRunAt: state.LastRunAt, LastRunID: state.LastRunID}
}

func readSchedule(item *unstructured.Unstructured) (workflowSchedule, error) {
	raw, ok := item.GetAnnotations()[scheduleAnnotation]
	if !ok {
		return workflowSchedule{}, ErrScheduleNotFound
	}
	var schedule workflowSchedule
	strict, err := kjson.UnmarshalStrict([]byte(raw), &schedule)
	if err != nil || len(strict) != 0 || schedule.UpdatedAt.IsZero() {
		return workflowSchedule{}, fmt.Errorf("invalid stored workflow schedule")
	}
	return schedule, nil
}

func readScheduleState(item *unstructured.Unstructured) (scheduleState, error) {
	raw, ok := item.GetAnnotations()[scheduleStateAnnotation]
	if !ok {
		return scheduleState{}, nil
	}
	var state scheduleState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return scheduleState{}, fmt.Errorf("invalid stored schedule state")
	}
	return state, nil
}

func setScheduleAnnotation(item *unstructured.Unstructured, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	annotations := item.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[key] = string(data)
	item.SetAnnotations(annotations)
	return nil
}
