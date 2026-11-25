// Package workflow defines workflow domain models, errors, and DAG validation.
package workflow

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("workflow not found")
	ErrConflict = errors.New("workflow state conflict")
	ErrInvalid  = errors.New("invalid workflow")
)

// Workflow represents a persisted workflow execution record.
type Workflow struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"`
	Result    string    `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IsValidTransition enforces allowed workflow status state machine transitions.
func IsValidTransition(fromStatus, toStatus, result, failure string) bool {
	if fromStatus == "queued" && toStatus == "running" {
		return result == "" && failure == ""
	}

	if fromStatus != "running" {
		return false
	}

	if toStatus == "succeeded" {
		return result != "" && failure == ""
	}

	if toStatus == "failed" {
		return result == "" && failure != ""
	}

	return false
}
