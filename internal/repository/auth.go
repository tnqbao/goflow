package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	TokenKindSession  = "session"
	TokenKindAPIToken = "api_token"

	sqliteTimestampFormat = "2006-01-02T15:04:05.000000000Z"
)

var (
	ErrUserNotFound       = errors.New("user not found")
	ErrAlreadyInitialized = errors.New("administrator account already initialized")
	ErrTokenNotFound      = errors.New("authentication token not found")
)

// User represents a persisted administrator account.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TokenRecord represents a persisted hashed session or API token.
type TokenRecord struct {
	ID         string
	UserID     string
	Name       string
	Kind       string
	TokenHash  string
	CSRFToken  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
}

// AuthenticatedToken pairs a validated token record with its owning user.
type AuthenticatedToken struct {
	TokenID   string
	UserID    string
	Username  string
	Kind      string
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// TokenSummary exposes non-sensitive metadata about an API token.
type TokenSummary struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// AuthRepository manages administrator accounts and hashed tokens in SQLite.
type AuthRepository struct {
	db *sql.DB
}

// NewAuthRepository wraps db in an AuthRepository.
func NewAuthRepository(db *sql.DB) *AuthRepository {
	return &AuthRepository{db: db}
}

// HasAnyUser reports whether the initial administrator account has been created.
func (r *AuthRepository) HasAnyUser(ctx context.Context) (bool, error) {
	if err := r.ensureReady(); err != nil {
		return false, err
	}

	var exists bool
	const query = `SELECT EXISTS(SELECT 1 FROM users LIMIT 1)`
	if err := r.db.QueryRowContext(ctx, query).Scan(&exists); err != nil {
		return false, fmt.Errorf("check administrator existence: %w", err)
	}

	return exists, nil
}

// CreateInitialAdmin atomically inserts the single initial administrator account.
func (r *AuthRepository) CreateInitialAdmin(ctx context.Context, user User) error {
	if err := r.ensureReady(); err != nil {
		return err
	}

	createdStr := formatTimestamp(user.CreatedAt)
	updatedStr := formatTimestamp(user.UpdatedAt)

	const query = `INSERT INTO users (id, singleton, username, password_hash, created_at, updated_at) VALUES (?, 1, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, query, user.ID, user.Username, user.PasswordHash, createdStr, updatedStr)
	if isUniqueConstraintError(err) {
		return ErrAlreadyInitialized
	}
	if err != nil {
		return fmt.Errorf("insert administrator: %w", err)
	}

	return nil
}

// FindUserByUsername looks up an administrator account by normalized username.
func (r *AuthRepository) FindUserByUsername(ctx context.Context, username string) (User, error) {
	if err := r.ensureReady(); err != nil {
		return User{}, err
	}

	const query = `SELECT id, username, password_hash, created_at, updated_at FROM users WHERE username = ?`
	var user User
	var createdStr, updatedStr string

	err := r.db.QueryRowContext(ctx, query, username).Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&createdStr,
		&updatedStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("query user: %w", err)
	}

	user.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return User{}, fmt.Errorf("parse user creation time: %w", err)
	}

	user.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedStr)
	if err != nil {
		return User{}, fmt.Errorf("parse user update time: %w", err)
	}

	return user, nil
}

// CreateToken inserts a hashed session or API token record.
func (r *AuthRepository) CreateToken(ctx context.Context, record TokenRecord) error {
	if err := r.ensureReady(); err != nil {
		return err
	}

	const query = `INSERT INTO auth_tokens (id, user_id, name, kind, token_hash, csrf_token, created_at, expires_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(
		ctx,
		query,
		record.ID,
		record.UserID,
		record.Name,
		record.Kind,
		record.TokenHash,
		record.CSRFToken,
		formatTimestamp(record.CreatedAt),
		formatTimestamp(record.ExpiresAt),
		formatTimestamp(record.LastUsedAt),
	)
	if err != nil {
		return fmt.Errorf("insert auth token: %w", err)
	}

	return nil
}

// FindValidToken resolves a SHA-256 token hash to its active user when unrevoked and unexpired.
func (r *AuthRepository) FindValidToken(ctx context.Context, tokenHash string, now time.Time) (AuthenticatedToken, error) {
	if err := r.ensureReady(); err != nil {
		return AuthenticatedToken{}, err
	}

	const query = `
		SELECT t.id, t.user_id, u.username, t.kind, t.csrf_token, u.created_at, t.expires_at
		FROM auth_tokens t
		INNER JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?
	`

	var auth AuthenticatedToken
	var userCreatedStr, expiresStr string
	err := r.db.QueryRowContext(ctx, query, tokenHash).Scan(
		&auth.TokenID,
		&auth.UserID,
		&auth.Username,
		&auth.Kind,
		&auth.CSRFToken,
		&userCreatedStr,
		&expiresStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthenticatedToken{}, ErrTokenNotFound
	}
	if err != nil {
		return AuthenticatedToken{}, fmt.Errorf("query auth token: %w", err)
	}

	auth.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresStr)
	if err != nil {
		return AuthenticatedToken{}, fmt.Errorf("parse token expiry: %w", err)
	}
	if !auth.ExpiresAt.After(now) {
		return AuthenticatedToken{}, ErrTokenNotFound
	}

	auth.CreatedAt, err = time.Parse(time.RFC3339Nano, userCreatedStr)
	if err != nil {
		return AuthenticatedToken{}, fmt.Errorf("parse user creation time: %w", err)
	}

	return auth, nil
}

// TouchToken updates last_used_at for a token ID.
func (r *AuthRepository) TouchToken(ctx context.Context, tokenID string, usedAt time.Time) error {
	if err := r.ensureReady(); err != nil {
		return err
	}

	const query = `UPDATE auth_tokens SET last_used_at = ? WHERE id = ?`
	if _, err := r.db.ExecContext(ctx, query, formatTimestamp(usedAt), tokenID); err != nil {
		return fmt.Errorf("touch auth token: %w", err)
	}

	return nil
}

// ListAPITokens returns active non-expired API token summaries for userID.
func (r *AuthRepository) ListAPITokens(ctx context.Context, userID string, now time.Time) ([]TokenSummary, error) {
	if err := r.ensureReady(); err != nil {
		return nil, err
	}

	const query = `
		SELECT id, name, created_at, expires_at, last_used_at
		FROM auth_tokens
		WHERE user_id = ? AND kind = ? AND expires_at > ?
		ORDER BY created_at DESC, id ASC
	`

	rows, err := r.db.QueryContext(ctx, query, userID, TokenKindAPIToken, formatTimestamp(now))
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}
	defer rows.Close()

	items := make([]TokenSummary, 0)
	for rows.Next() {
		summary, scanErr := scanTokenSummary(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, summary)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate API tokens: %w", err)
	}

	return items, nil
}

// RevokeTokenByHash deletes a session or API token matching tokenHash.
func (r *AuthRepository) RevokeTokenByHash(ctx context.Context, tokenHash string) error {
	if err := r.ensureReady(); err != nil {
		return err
	}

	const query = `DELETE FROM auth_tokens WHERE token_hash = ?`
	if _, err := r.db.ExecContext(ctx, query, tokenHash); err != nil {
		return fmt.Errorf("revoke auth token: %w", err)
	}

	return nil
}

// RevokeAPITokenByID deletes a specific API token owned by userID.
func (r *AuthRepository) RevokeAPITokenByID(ctx context.Context, userID, tokenID string) error {
	if err := r.ensureReady(); err != nil {
		return err
	}

	const query = `DELETE FROM auth_tokens WHERE id = ? AND user_id = ? AND kind = ?`
	res, err := r.db.ExecContext(ctx, query, tokenID, userID, TokenKindAPIToken)
	if err != nil {
		return fmt.Errorf("delete API token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted API tokens: %w", err)
	}
	if affected == 0 {
		return ErrTokenNotFound
	}

	return nil
}

// DeleteExpiredTokens prunes expired session and API tokens.
func (r *AuthRepository) DeleteExpiredTokens(ctx context.Context, now time.Time) error {
	if err := r.ensureReady(); err != nil {
		return err
	}

	const query = `DELETE FROM auth_tokens WHERE expires_at <= ?`
	if _, err := r.db.ExecContext(ctx, query, formatTimestamp(now)); err != nil {
		return fmt.Errorf("prune expired tokens: %w", err)
	}

	return nil
}

func (r *AuthRepository) ensureReady() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("auth database is unavailable")
	}

	return nil
}

func scanTokenSummary(row rowScanner) (TokenSummary, error) {
	var item TokenSummary
	var createdStr, expiresStr, usedStr string

	if err := row.Scan(&item.ID, &item.Name, &createdStr, &expiresStr, &usedStr); err != nil {
		return TokenSummary{}, fmt.Errorf("scan token summary: %w", err)
	}

	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return TokenSummary{}, fmt.Errorf("parse token creation time: %w", err)
	}

	item.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresStr)
	if err != nil {
		return TokenSummary{}, fmt.Errorf("parse token expiry time: %w", err)
	}

	item.LastUsedAt, err = time.Parse(time.RFC3339Nano, usedStr)
	if err != nil {
		return TokenSummary{}, fmt.Errorf("parse token last-used time: %w", err)
	}

	return item, nil
}

func formatTimestamp(ts time.Time) string {
	return ts.UTC().Format(sqliteTimestampFormat)
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") || strings.Contains(msg, "constraint failed: unique")
}
