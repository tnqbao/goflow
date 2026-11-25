package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// cleanupArtifacts expires published artifacts belonging to completed, successful runs.
func (e *StepExecutor) cleanupArtifacts(ctx context.Context, run *unstructured.Unstructured, now time.Time) error {
	if e.Artifacts == nil {
		return fmt.Errorf("artifact storage is unavailable")
	}

	completedStr, found, err := unstructured.NestedString(run.Object, "status", "completedAt")
	if err != nil {
		return fmt.Errorf("read completion time: %w", err)
	}
	if !found {
		return nil
	}

	completedAt, err := time.Parse(time.RFC3339Nano, completedStr)
	if err != nil {
		return fmt.Errorf("parse completion time: %w", err)
	}

	artifactKeys, _, err := unstructured.NestedStringSlice(run.Object, "status", "artifacts")
	if err != nil {
		return fmt.Errorf("read artifacts: %w", err)
	}

	allowedKeys, err := e.collectAllowedRunArtifacts(run)
	if err != nil {
		return err
	}

	for _, key := range artifactKeys {
		if !allowedKeys[key] {
			return fmt.Errorf("run references an unexpected artifact")
		}
	}

	for _, key := range artifactKeys {
		_, err := e.Artifacts.DeleteExpired(ctx, key, completedAt, now)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("expire artifact: %w", err)
		}
	}

	return nil
}

func (e *StepExecutor) collectAllowedRunArtifacts(run *unstructured.Unstructured) (map[string]bool, error) {
	steps, err := executableSteps(run)
	if err != nil {
		return nil, err
	}

	status, _, err := unstructured.NestedMap(run.Object, "status")
	if err != nil {
		return nil, err
	}

	_, byName, err := stepStatuses(status)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool, len(steps))
	for _, step := range steps {
		entry := byName[step.Name]
		if entry == nil || entry["phase"] != "Succeeded" {
			return nil, fmt.Errorf("successful run has incomplete step %q", step.Name)
		}

		attempt, err := stepAttempt(entry)
		if err != nil {
			return nil, err
		}

		jobCfg, err := recordedJobConfig(entry, e.Config)
		if err != nil {
			return nil, err
		}

		job, err := stepJobForStep(run.GetName(), run.GetUID(), step, attempt, jobCfg)
		if err != nil {
			return nil, err
		}

		if entry["jobName"] != job.Name {
			return nil, fmt.Errorf("step %q has invalid job identity", step.Name)
		}

		artifactKey := stepOutputArtifactKey(step, job.Labels["kubereel.io/workflow"])
		allowed[artifactKey] = true
	}

	return allowed, nil
}
