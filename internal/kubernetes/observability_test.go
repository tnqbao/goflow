package kubernetes

import (
	"testing"
	"time"
)

func TestRunTimestampsFollowAttemptsAndTerminalState(t *testing.T) {
	start := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	first := map[string]any{"name": "image", "phase": "Running", "attempts": int64(1)}
	running := map[string]any{"phase": "Running", "steps": []any{first}}
	stampRunStatus(nil, running, start)
	if first["startedAt"] != start.Format(time.RFC3339Nano) || running["startedAt"] != first["startedAt"] {
		t.Fatal("missing or inconsistent start timestamps")
	}

	failedAttempt := map[string]any{"name": "image", "phase": "Pending", "attempts": int64(1), "startedAt": first["startedAt"]}
	pending := map[string]any{"phase": "Running", "startedAt": running["startedAt"], "steps": []any{failedAttempt}}
	stampRunStatus(running, pending, start.Add(time.Second))
	if statusElapsed(failedAttempt, time.Time{}) != time.Second {
		t.Fatal("failed attempt duration was lost")
	}

	retry := map[string]any{"name": "image", "phase": "Running", "attempts": int64(2), "startedAt": first["startedAt"], "completedAt": failedAttempt["completedAt"]}
	retrying := map[string]any{"phase": "Running", "startedAt": running["startedAt"], "steps": []any{retry}}
	at := start.Add(2 * time.Second)
	stampRunStatus(pending, retrying, at)
	if retry["startedAt"] != at.Format(time.RFC3339Nano) || retry["completedAt"] != nil {
		t.Fatal("retry did not get a fresh attempt start")
	}

	failed := map[string]any{"phase": "Failed", "startedAt": retrying["startedAt"], "steps": []any{retry}}
	stampRunStatus(retrying, failed, start.Add(3*time.Second))
	if failed["completedAt"] != nil {
		t.Fatal("run marked complete while a step is active")
	}
}
