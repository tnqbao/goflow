// Package repository implements persistence adapters for KubeReel domain models.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tnqbao/kubereel/internal/workflow"
)

// WorkflowRepository manages workflow persistence in SQLite.
type WorkflowRepository struct {
	db *sql.DB
}

// NewWorkflowRepository wraps db in a WorkflowRepository.
func NewWorkflowRepository(db *sql.DB) *WorkflowRepository {
	return &WorkflowRepository{db: db}
}

// Insert persists a new workflow record.
func (r *WorkflowRepository) Insert(ctx context.Context, wf workflow.Workflow) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("workflow database is unavailable")
	}

	createdStr := wf.CreatedAt.UTC().Format(time.RFC3339Nano)
	updatedStr := wf.UpdatedAt.UTC().Format(time.RFC3339Nano)

	const query = `INSERT INTO workflows (id, prompt, status, result, error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, query, wf.ID, wf.Prompt, wf.Status, wf.Result, wf.Error, createdStr, updatedStr); err != nil {
		return fmt.Errorf("insert workflow: %w", err)
	}

	return nil
}

// FindByID retrieves a single workflow record by ID.
func (r *WorkflowRepository) FindByID(ctx context.Context, id string) (workflow.Workflow, error) {
	if r == nil || r.db == nil {
		return workflow.Workflow{}, fmt.Errorf("workflow database is unavailable")
	}

	const query = `SELECT id, prompt, status, result, error, created_at, updated_at FROM workflows WHERE id = ?`
	wf, err := scanWorkflow(r.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Workflow{}, workflow.ErrNotFound
	}

	return wf, err
}

// ListActive returns queued and running workflows ordered by creation time.
func (r *WorkflowRepository) ListActive(ctx context.Context) ([]workflow.Workflow, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("workflow database is unavailable")
	}

	const query = `SELECT id, prompt, status, result, error, created_at, updated_at FROM workflows WHERE status IN ('queued', 'running') ORDER BY created_at, id`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}
	defer rows.Close()

	var items []workflow.Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, wf)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflows: %w", err)
	}

	return items, nil
}

// UpdateStatus atomically updates a workflow when its current status matches fromStatus.
func (r *WorkflowRepository) UpdateStatus(
	ctx context.Context,
	id, fromStatus, toStatus, result, failure string,
	updatedAt time.Time,
) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("workflow database is unavailable")
	}

	updatedStr := updatedAt.UTC().Format(time.RFC3339Nano)
	const query = `UPDATE workflows SET status = ?, result = ?, error = ?, updated_at = ? WHERE id = ? AND status = ?`

	res, err := r.db.ExecContext(ctx, query, toStatus, result, failure, updatedStr, id, fromStatus)
	if err != nil {
		return fmt.Errorf("update workflow status: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated workflows: %w", err)
	}
	if affected == 0 {
		return workflow.ErrConflict
	}

	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(row rowScanner) (workflow.Workflow, error) {
	var wf workflow.Workflow
	var createdStr, updatedStr string

	err := row.Scan(&wf.ID, &wf.Prompt, &wf.Status, &wf.Result, &wf.Error, &createdStr, &updatedStr)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("read workflow: %w", err)
	}

	wf.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("parse creation time: %w", err)
	}

	wf.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedStr)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("parse update time: %w", err)
	}

	return wf, nil
}
