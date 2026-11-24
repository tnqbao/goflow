// Package workflow stores workflow execution state.
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

var (
	ErrNotFound = errors.New("workflow not found")
	ErrConflict = errors.New("workflow state conflict")
	ErrInvalid  = errors.New("invalid workflow")
)

type Workflow struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"`
	Result    string    `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, prompt string) (Workflow, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len(prompt) > 8192 {
		return Workflow{}, fmt.Errorf("%w: prompt must contain 1 to 8192 bytes", ErrInvalid)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Workflow{}, fmt.Errorf("generate workflow ID: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	workflowID := hex.EncodeToString(id[:])
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflows (id, prompt, status, created_at, updated_at) VALUES (?, ?, 'queued', ?, ?)`, workflowID, prompt, now, now)
	if err != nil {
		return Workflow{}, fmt.Errorf("create workflow: %w", err)
	}
	return s.Get(ctx, workflowID)
}

func (s *Store) Get(ctx context.Context, id string) (Workflow, error) {
	w, err := scanWorkflow(s.db.QueryRowContext(ctx, `SELECT id, prompt, status, result, error, created_at, updated_at FROM workflows WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, ErrNotFound
	}
	return w, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(row scanner) (Workflow, error) {
	var w Workflow
	var created, updated string
	err := row.Scan(&w.ID, &w.Prompt, &w.Status, &w.Result, &w.Error, &created, &updated)
	if err != nil {
		return Workflow{}, fmt.Errorf("read workflow: %w", err)
	}
	w.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Workflow{}, fmt.Errorf("parse creation time: %w", err)
	}
	w.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Workflow{}, fmt.Errorf("parse update time: %w", err)
	}
	return w, nil
}

// Active returns workflows that still need scheduler reconciliation.
func (s *Store) Active(ctx context.Context) ([]Workflow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, prompt, status, result, error, created_at, updated_at FROM workflows WHERE status IN ('queued', 'running') ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}
	defer rows.Close()
	var workflows []Workflow
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflows: %w", err)
	}
	return workflows, nil
}

// Transition changes the workflow only when it has the expected current status.
func (s *Store) Transition(ctx context.Context, id, from, to, result, failure string) error {
	if !validTransition(from, to, result, failure) {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE workflows SET status = ?, result = ?, error = ?, updated_at = ? WHERE id = ? AND status = ?`, to, result, failure, time.Now().UTC().Format(time.RFC3339Nano), id, from)
	if err != nil {
		return fmt.Errorf("update workflow: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count workflow updates: %w", err)
	}
	if count == 0 {
		return ErrConflict
	}
	return nil
}

func validTransition(from, to, result, failure string) bool {
	if from == "queued" && to == "running" {
		return result == "" && failure == ""
	}
	if from != "running" {
		return false
	}
	if to == "succeeded" {
		return result != "" && failure == ""
	}
	if to == "failed" {
		return result == "" && failure != ""
	}
	return false
}
