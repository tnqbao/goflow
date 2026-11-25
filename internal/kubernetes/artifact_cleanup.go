package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// cleanupArtifacts only considers published artifacts of completed, successful runs.
func (e *StepExecutor) cleanupArtifacts(ctx context.Context, run *unstructured.Unstructured, now time.Time) error {
	if e.Artifacts == nil {
		return fmt.Errorf("artifact storage is unavailable")
	}
	completed, found, err := unstructured.NestedString(run.Object, "status", "completedAt")
	if err != nil {
		return fmt.Errorf("read completion time: %w", err)
	}
	if !found {
		return nil
	}
	completedAt, err := time.Parse(time.RFC3339Nano, completed)
	if err != nil {
		return fmt.Errorf("parse completion time: %w", err)
	}
	artifacts, _, err := unstructured.NestedStringSlice(run.Object, "status", "artifacts")
	if err != nil {
		return fmt.Errorf("read artifacts: %w", err)
	}
	steps, err := executableSteps(run)
	if err != nil {
		return err
	}
	status, _, err := unstructured.NestedMap(run.Object, "status")
	if err != nil {
		return err
	}
	_, statuses, err := stepStatuses(status)
	if err != nil {
		return err
	}
	allowed := make(map[string]bool, len(steps))
	for _, step := range steps {
		entry := statuses[step.Name]
		if entry == nil || entry["phase"] != "Succeeded" {
			return fmt.Errorf("successful run has incomplete step %q", step.Name)
		}
		attempt, err := stepAttempt(entry)
		if err != nil {
			return err
		}
		cfg, err := recordedJobConfig(entry, e.Config)
		if err != nil {
			return err
		}
		job, err := StepJob(run.GetName(), run.GetUID(), step.Name, attempt, cfg)
		if err != nil {
			return err
		}
		if entry["jobName"] != job.Name {
			return fmt.Errorf("step %q has invalid job identity", step.Name)
		}
		allowed[path.Join("workflows", job.Labels["kubereel.io/workflow"], "scene.svg")] = true
	}
	for _, key := range artifacts {
		if !allowed[key] {
			return fmt.Errorf("run references an unexpected artifact")
		}
	}
	for _, key := range artifacts {
		_, err := e.Artifacts.DeleteExpired(ctx, key, completedAt, now)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("expire artifact: %w", err)
		}
	}
	return nil
}
