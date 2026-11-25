package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/database"
)

func TestAuthRepositoryAdminSingletonAndTokens(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewAuthRepository(db)

	initialized, err := repo.HasAnyUser(ctx)
	if err != nil || initialized {
		t.Fatalf("HasAnyUser before setup = %v, %v", initialized, err)
	}

	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	admin := User{
		ID:           "usr-1",
		Username:     "admin",
		PasswordHash: "$2a$10$examplehash",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.CreateInitialAdmin(ctx, admin); err != nil {
		t.Fatalf("CreateInitialAdmin: %v", err)
	}

	second := User{
		ID:           "usr-2",
		Username:     "other",
		PasswordHash: "$2a$10$otherhash",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.CreateInitialAdmin(ctx, second); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second CreateInitialAdmin err = %v, want ErrAlreadyInitialized", err)
	}

	initialized, err = repo.HasAnyUser(ctx)
	if err != nil || !initialized {
		t.Fatalf("HasAnyUser after setup = %v, %v", initialized, err)
	}

	found, err := repo.FindUserByUsername(ctx, "admin")
	if err != nil || found.ID != "usr-1" {
		t.Fatalf("FindUserByUsername = %+v, %v", found, err)
	}

	if _, err := repo.FindUserByUsername(ctx, "missing"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing user err = %v, want ErrUserNotFound", err)
	}

	sessionRec := TokenRecord{
		ID:         "tok-session",
		UserID:     "usr-1",
		Name:       "browser",
		Kind:       TokenKindSession,
		TokenHash:  "hash-session",
		CSRFToken:  "csrf-secret",
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Hour),
		LastUsedAt: now,
	}
	if err := repo.CreateToken(ctx, sessionRec); err != nil {
		t.Fatalf("CreateToken session: %v", err)
	}

	apiRec := TokenRecord{
		ID:         "tok-api",
		UserID:     "usr-1",
		Name:       "ci-runner",
		Kind:       TokenKindAPIToken,
		TokenHash:  "hash-api",
		CreatedAt:  now,
		ExpiresAt:  now.Add(24 * time.Hour),
		LastUsedAt: now,
	}
	if err := repo.CreateToken(ctx, apiRec); err != nil {
		t.Fatalf("CreateToken api: %v", err)
	}

	valid, err := repo.FindValidToken(ctx, "hash-session", now.Add(10*time.Minute))
	if err != nil || valid.Username != "admin" || valid.CSRFToken != "csrf-secret" {
		t.Fatalf("FindValidToken = %+v, %v", valid, err)
	}

	if _, err := repo.FindValidToken(ctx, "hash-session", now.Add(2*time.Hour)); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("expired token err = %v, want ErrTokenNotFound", err)
	}

	tokens, err := repo.ListAPITokens(ctx, "usr-1", now)
	if err != nil || len(tokens) != 1 || tokens[0].ID != "tok-api" || tokens[0].Name != "ci-runner" {
		t.Fatalf("ListAPITokens = %+v, %v", tokens, err)
	}

	if err := repo.RevokeAPITokenByID(ctx, "usr-1", "tok-api"); err != nil {
		t.Fatalf("RevokeAPITokenByID: %v", err)
	}
	if err := repo.RevokeAPITokenByID(ctx, "usr-1", "tok-api"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("second RevokeAPITokenByID err = %v, want ErrTokenNotFound", err)
	}

	if err := repo.DeleteExpiredTokens(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("DeleteExpiredTokens: %v", err)
	}
	if _, err := repo.FindValidToken(ctx, "hash-session", now); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("pruned token err = %v, want ErrTokenNotFound", err)
	}
}
