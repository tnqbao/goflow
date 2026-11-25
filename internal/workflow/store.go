// Package workflow stores workflow execution state and validates DAGs.
package workflow

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxPromptBytes = 8192

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

// Store manages workflow records in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore wraps db in a workflow Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Create validates prompt and inserts a new queued workflow.
func (s *Store) Create(ctx context.Context, prompt string) (Workflow, error) {
	trimmedPrompt := strings.TrimSpace(prompt)
	if trimmedPrompt == "" || len(trimmedPrompt) > maxPromptBytes {
		return Workflow{}, fmt.Errorf("%w: prompt must contain 1 to %d bytes", ErrInvalid, maxPromptBytes)
	}

	var rawID [16]byte
	if _, err := rand.Read(rawID[:]); err != nil {
		return Workflow{}, fmt.Errorf("generate workflow ID: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	workflowID := hex.EncodeToString(rawID[:])

	const insertQuery = `INSERT INTO workflows (id, prompt, status, created_at, updated_at) VALUES (?, ?, 'queued', ?, ?)`
	if _, err := s.db.ExecContext(ctx, insertQuery, workflowID, trimmedPrompt, now, now); err != nil {
		return Workflow{}, fmt.Errorf("create workflow: %w", err)
	}

	return s.Get(ctx, workflowID)
}

// Get retrieves a workflow by ID.
func (s *Store) Get(ctx context.Context, id string) (Workflow, error) {
	const selectQuery = `SELECT id, prompt, status, result, error, created_at, updated_at FROM workflows WHERE id = ?`

	wf, err := scanWorkflow(s.db.QueryRowContext(ctx, selectQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, ErrNotFound
	}

	return wf, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(row rowScanner) (Workflow, error) {
	var wf Workflow
	var createdStr, updatedStr string

	err := row.Scan(&wf.ID, &wf.Prompt, &wf.Status, &wf.Result, &wf.Error, &createdStr, &updatedStr)
	if err != nil {
		return Workflow{}, fmt.Errorf("read workflow: %w", err)
	}

	wf.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return Workflow{}, fmt.Errorf("parse creation time: %w", err)
	}

	wf.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedStr)
	if err != nil {
		return Workflow{}, fmt.Errorf("parse update time: %w", err)
	}

	return wf, nil
}

// Active returns workflows that still need scheduler reconciliation.
func (s *Store) Active(ctx context.Context) ([]Workflow, error) {
	const activeQuery = `SELECT id, prompt, status, result, error, created_at, updated_at FROM workflows WHERE status IN ('queued', 'running') ORDER BY created_at, id`

	rows, err := s.db.QueryContext(ctx, activeQuery)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}
	defer rows.Close()

	var workflows []Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, wf)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflows: %w", err)
	}

	return workflows, nil
}

// Transition updates the workflow state only when its current status matches fromStatus.
func (s *Store) Transition(ctx context.Context, id, fromStatus, toStatus, result, failure string) error {
	if !isValidTransition(fromStatus, toStatus, result, failure) {
		return ErrInvalid
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	const updateQuery = `UPDATE workflows SET status = ?, result = ?, error = ?, updated_at = ? WHERE id = ? AND status = ?`

	res, err := s.db.ExecContext(ctx, updateQuery, toStatus, result, failure, now, id, fromStatus)
	if err != nil {
		return fmt.Errorf("update workflow: %w", err)
	}

	updatedRows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count workflow updates: %w", err)
	}

	if updatedRows == 0 {
		return ErrConflict
	}

	return nil
}

func isValidTransition(fromStatus, toStatus, result, failure string) bool {
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
