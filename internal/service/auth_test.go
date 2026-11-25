package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

func TestAuthServiceSetupLoginRateLimitAndTokens(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := NewAuthService(repository.NewAuthRepository(db), config.AuthConfig{
		SessionTTL: time.Hour,
		SetupToken: "cluster-setup-secret",
	})
	svc.bcryptCost = bcrypt.MinCost

	clock := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }

	if _, _, err := svc.Login(ctx, "127.0.0.1", "admin", "valid-password-123", repository.TokenKindSession); !errors.Is(err, ErrSetupRequired) {
		t.Fatalf("Login before setup err = %v, want ErrSetupRequired", err)
	}

	if _, err := svc.Setup(ctx, "admin", "valid-password-123", "wrong-token", repository.TokenKindSession); !errors.Is(err, ErrInvalidSetupToken) {
		t.Fatalf("Setup with bad setup token err = %v, want ErrInvalidSetupToken", err)
	}

	if _, err := svc.Setup(ctx, "admin", "too-short", "cluster-setup-secret", repository.TokenKindSession); !errors.Is(err, ErrInvalidAuthInput) {
		t.Fatalf("Setup with weak password err = %v, want ErrInvalidAuthInput", err)
	}

	initial, err := svc.Setup(ctx, "Admin_One", "correct-horse-battery-123", "cluster-setup-secret", repository.TokenKindSession)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if initial.User.Username != "admin_one" || initial.CSRFToken == "" || initial.Token == "" {
		t.Fatalf("unexpected initial auth = %+v", initial)
	}

	if _, err := svc.Setup(ctx, "admin2", "correct-horse-battery-123", "cluster-setup-secret", repository.TokenKindSession); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second Setup err = %v, want ErrAlreadyInitialized", err)
	}

	principal, err := svc.Authenticate(ctx, initial.Token)
	if err != nil || principal.Username != "admin_one" {
		t.Fatalf("Authenticate initial session = %+v, %v", principal, err)
	}
	if !svc.VerifyCSRF(principal, initial.CSRFToken) || svc.VerifyCSRF(principal, "forged-csrf") {
		t.Fatal("CSRF verification behaved unexpectedly")
	}

	// Exhaust failure limiter from 10.0.0.9.
	for attempt := 0; attempt < defaultLoginMaxFailures; attempt++ {
		if _, _, err := svc.Login(ctx, "10.0.0.9", "admin_one", "wrong-password-123", repository.TokenKindAPIToken); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("bad password attempt %d err = %v", attempt, err)
		}
	}
	if _, retryAfter, err := svc.Login(ctx, "10.0.0.9", "admin_one", "correct-horse-battery-123", repository.TokenKindAPIToken); !errors.Is(err, ErrLoginRateLimited) || retryAfter <= 0 {
		t.Fatalf("expected ErrLoginRateLimited, got err=%v retryAfter=%v", err, retryAfter)
	}

	// Different IP can still sign in, and advancing past the window unblocks 10.0.0.9.
	loggedIn, _, err := svc.Login(ctx, "10.0.0.10", "ADMIN_ONE", "correct-horse-battery-123", repository.TokenKindAPIToken)
	if err != nil || loggedIn.Token == "" {
		t.Fatalf("Login from clean IP = %+v, %v", loggedIn, err)
	}

	apiToken, err := svc.CreateAPIToken(ctx, principal, "github-actions", 3600)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	listed, err := svc.ListAPITokens(ctx, principal.UserID)
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListAPITokens = %+v, %v", listed, err)
	}
	if err := svc.RevokeAPIToken(ctx, principal.UserID, apiToken.TokenID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := svc.Authenticate(ctx, apiToken.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked API token err = %v, want ErrUnauthorized", err)
	}

	if err := svc.Logout(ctx, initial.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, initial.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("logged-out session err = %v, want ErrUnauthorized", err)
	}

	// Advance clock past session/token TTL and verify expiry rejection.
	clock = clock.Add(31 * 24 * time.Hour)
	if _, err := svc.Authenticate(ctx, loggedIn.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired API token err = %v, want ErrUnauthorized", err)
	}
}

func TestAuthServiceBootstrapIdempotency(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "bootstrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := NewAuthService(repository.NewAuthRepository(db), config.AuthConfig{
		SessionTTL:        time.Hour,
		BootstrapUsername: "ops-admin",
		BootstrapPassword: "bootstrap-secret-password",
	})
	svc.bcryptCost = bcrypt.MinCost

	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}
	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}

	issued, _, err := svc.Login(ctx, "127.0.0.1", "ops-admin", "bootstrap-secret-password", repository.TokenKindAPIToken)
	if err != nil || issued.User.Username != "ops-admin" {
		t.Fatalf("Login after Bootstrap = %+v, %v", issued, err)
	}
}
