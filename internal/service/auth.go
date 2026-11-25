package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookieName = "kubereel_session"

	minPasswordBytes     = 12
	maxPasswordBytes     = 72
	maxTokenNameBytes    = 64
	defaultAPITokenTTL   = 30 * 24 * time.Hour
	minAPITokenSeconds   = 60
	maxAPITokenSeconds   = 365 * 24 * 3600
	sessionTokenPrefix   = "krs_"
	apiTokenPrefix       = "krt_"
	tokenEntropyBytes    = 32
	identifierRandomByte = 16
)

var (
	ErrSetupRequired        = errors.New("initial administrator setup is required")
	ErrAlreadyInitialized   = repository.ErrAlreadyInitialized
	ErrInvalidSetupToken    = errors.New("invalid initial setup token")
	ErrInvalidCredentials   = errors.New("invalid username or password")
	ErrUnauthorized         = errors.New("authentication required")
	ErrInvalidAuthInput     = errors.New("invalid authentication input")
	ErrLoginRateLimited     = errors.New("too many failed sign-in attempts")
	ErrAuthTokenNotFound    = repository.ErrTokenNotFound
	ErrAuthStoreUnavailable = errors.New("authentication store is unavailable")

	usernamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
	tokenNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9 ._-]{0,63}$`)

	// Precomputed bcrypt digest used to equalize timing when a username does not exist.
	dummyPasswordHash = mustGenerateDummyHash()
)

// UserProfile exposes non-sensitive administrator metadata.
type UserProfile struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
}

// Principal describes an authenticated caller attached to a request context.
type Principal struct {
	UserID    string
	Username  string
	CreatedAt time.Time
	TokenID   string
	Kind      string
	CSRFToken string
	ViaCookie bool
}

// IssuedAuth holds a newly minted raw token and its metadata.
type IssuedAuth struct {
	TokenID   string      `json:"tokenId"`
	Token     string      `json:"token"`
	CSRFToken string      `json:"-"`
	ExpiresAt time.Time   `json:"expiresAt"`
	User      UserProfile `json:"user"`
}

// AuthStatus summarizes system bootstrap state and optional caller identity.
type AuthStatus struct {
	Initialized        bool   `json:"initialized"`
	SetupTokenRequired bool   `json:"setupTokenRequired,omitempty"`
	Authenticated      bool   `json:"authenticated"`
	Username           string `json:"username,omitempty"`
	CSRFToken          string `json:"csrfToken,omitempty"`
}

// AuthService manages initial admin setup, credential verification, sessions, and API tokens.
type AuthService struct {
	repo       *repository.AuthRepository
	cfg        config.AuthConfig
	bcryptCost int
	limiter    *loginRateLimiter
	now        func() time.Time
}

// NewAuthService creates an AuthService backed by repo and cfg.
func NewAuthService(repo *repository.AuthRepository, cfg config.AuthConfig) *AuthService {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 24 * time.Hour
	}

	return &AuthService{
		repo:       repo,
		cfg:        cfg,
		bcryptCost: bcrypt.DefaultCost,
		limiter:    newLoginRateLimiter(defaultLoginMaxFailures, defaultLoginWindow),
		now:        time.Now,
	}
}

// SessionTTL returns the configured browser session lifetime.
func (s *AuthService) SessionTTL() time.Duration {
	if s == nil || s.cfg.SessionTTL <= 0 {
		return 24 * time.Hour
	}

	return s.cfg.SessionTTL
}

// RequiresSetupToken reports whether initial setup requires an operator-configured setup token.
func (s *AuthService) RequiresSetupToken() bool {
	return s != nil && s.cfg.SetupToken != ""
}

// IsInitialized reports whether the initial administrator account exists.
func (s *AuthService) IsInitialized(ctx context.Context) (bool, error) {
	if s == nil || s.repo == nil {
		return false, ErrAuthStoreUnavailable
	}

	return s.repo.HasAnyUser(ctx)
}

// Bootstrap idempotently provisions the administrator account when bootstrap credentials are set.
func (s *AuthService) Bootstrap(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return ErrAuthStoreUnavailable
	}
	if s.cfg.BootstrapPassword == "" {
		return nil
	}

	initialized, err := s.repo.HasAnyUser(ctx)
	if err != nil || initialized {
		return err
	}

	_, err = s.createAdminAccount(ctx, s.cfg.BootstrapUsername, s.cfg.BootstrapPassword)
	if errors.Is(err, ErrAlreadyInitialized) {
		return nil
	}

	return err
}

// Status returns system initialization state and resolves an optional caller token.
func (s *AuthService) Status(ctx context.Context, rawToken string, viaCookie bool) (AuthStatus, error) {
	initialized, err := s.IsInitialized(ctx)
	if err != nil {
		return AuthStatus{}, err
	}

	status := AuthStatus{
		Initialized:        initialized,
		SetupTokenRequired: !initialized && s.RequiresSetupToken(),
	}
	if !initialized || strings.TrimSpace(rawToken) == "" {
		return status, nil
	}

	principal, authErr := s.Authenticate(ctx, rawToken)
	if authErr != nil {
		return status, nil
	}

	status.Authenticated = true
	status.Username = principal.Username
	if viaCookie {
		status.CSRFToken = principal.CSRFToken
	}

	return status, nil
}

// Setup performs one-time administrator initialization and mints an initial session or API token.
func (s *AuthService) Setup(
	ctx context.Context,
	username, password, setupToken, tokenKind string,
) (IssuedAuth, error) {
	if s == nil || s.repo == nil {
		return IssuedAuth{}, ErrAuthStoreUnavailable
	}
	if !s.verifySetupToken(setupToken) {
		return IssuedAuth{}, ErrInvalidSetupToken
	}

	initialized, err := s.repo.HasAnyUser(ctx)
	if err != nil {
		return IssuedAuth{}, err
	}
	if initialized {
		return IssuedAuth{}, ErrAlreadyInitialized
	}

	user, err := s.createAdminAccount(ctx, username, password)
	if err != nil {
		return IssuedAuth{}, err
	}

	return s.issueToken(ctx, user, "initial", normalizeTokenKind(tokenKind), s.ttlForKind(tokenKind))
}

// Login verifies administrator credentials with rate-limit and timing-attack protections.
func (s *AuthService) Login(
	ctx context.Context,
	clientKey, username, password, tokenKind string,
) (IssuedAuth, time.Duration, error) {
	if s == nil || s.repo == nil {
		return IssuedAuth{}, 0, ErrAuthStoreUnavailable
	}

	now := s.now().UTC()
	if retryAfter, allowed := s.limiter.check(clientKey, now); !allowed {
		return IssuedAuth{}, retryAfter, ErrLoginRateLimited
	}

	initialized, err := s.repo.HasAnyUser(ctx)
	if err != nil {
		return IssuedAuth{}, 0, err
	}
	if !initialized {
		return IssuedAuth{}, 0, ErrSetupRequired
	}

	normalizedUser := normalizeUsername(username)
	if normalizedUser == "" || password == "" || len(password) > maxPasswordBytes {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte("invalid-password"))
		s.limiter.recordFailure(clientKey, now)
		return IssuedAuth{}, 0, ErrInvalidCredentials
	}

	user, err := s.repo.FindUserByUsername(ctx, normalizedUser)
	if errors.Is(err, repository.ErrUserNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		s.limiter.recordFailure(clientKey, now)
		return IssuedAuth{}, 0, ErrInvalidCredentials
	}
	if err != nil {
		return IssuedAuth{}, 0, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		s.limiter.recordFailure(clientKey, now)
		return IssuedAuth{}, 0, ErrInvalidCredentials
	}

	s.limiter.reset(clientKey)
	_ = s.repo.DeleteExpiredTokens(ctx, now)

	kind := normalizeTokenKind(tokenKind)
	issued, err := s.issueToken(ctx, user, kind, kind, s.ttlForKind(kind))
	if err != nil {
		return IssuedAuth{}, 0, err
	}

	return issued, 0, nil
}

// Authenticate validates a raw session or API token and returns its owning Principal.
func (s *AuthService) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	if s == nil || s.repo == nil {
		return Principal{}, ErrAuthStoreUnavailable
	}

	trimmed := strings.TrimSpace(rawToken)
	if !hasValidTokenFormat(trimmed) {
		return Principal{}, ErrUnauthorized
	}

	now := s.now().UTC()
	record, err := s.repo.FindValidToken(ctx, hashRawToken(trimmed), now)
	if errors.Is(err, repository.ErrTokenNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}

	_ = s.repo.TouchToken(ctx, record.TokenID, now)

	return Principal{
		UserID:    record.UserID,
		Username:  record.Username,
		CreatedAt: record.CreatedAt,
		TokenID:   record.TokenID,
		Kind:      record.Kind,
		CSRFToken: record.CSRFToken,
	}, nil
}

// VerifyCSRF checks a submitted CSRF token against a cookie-authenticated Principal in constant time.
func (s *AuthService) VerifyCSRF(principal Principal, candidate string) bool {
	expected := strings.TrimSpace(principal.CSRFToken)
	actual := strings.TrimSpace(candidate)
	if expected == "" || len(expected) != len(actual) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

// Logout revokes a raw session or API token.
func (s *AuthService) Logout(ctx context.Context, rawToken string) error {
	if s == nil || s.repo == nil {
		return ErrAuthStoreUnavailable
	}

	trimmed := strings.TrimSpace(rawToken)
	if !hasValidTokenFormat(trimmed) {
		return nil
	}

	return s.repo.RevokeTokenByHash(ctx, hashRawToken(trimmed))
}

// CreateAPIToken mints a named personal API token with bounded expiration.
func (s *AuthService) CreateAPIToken(
	ctx context.Context,
	principal Principal,
	name string,
	expiresInSeconds int64,
) (IssuedAuth, error) {
	if s == nil || s.repo == nil {
		return IssuedAuth{}, ErrAuthStoreUnavailable
	}

	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || len(trimmedName) > maxTokenNameBytes || !tokenNamePattern.MatchString(trimmedName) {
		return IssuedAuth{}, fmt.Errorf("%w: token name must be 1 to 64 alphanumeric characters", ErrInvalidAuthInput)
	}

	ttl, err := resolveAPITokenTTL(expiresInSeconds)
	if err != nil {
		return IssuedAuth{}, err
	}

	user := repository.User{
		ID:        principal.UserID,
		Username:  principal.Username,
		CreatedAt: principal.CreatedAt,
	}

	return s.issueToken(ctx, user, trimmedName, repository.TokenKindAPIToken, ttl)
}

// ListAPITokens returns non-sensitive summaries of active API tokens owned by userID.
func (s *AuthService) ListAPITokens(ctx context.Context, userID string) ([]repository.TokenSummary, error) {
	if s == nil || s.repo == nil {
		return nil, ErrAuthStoreUnavailable
	}

	return s.repo.ListAPITokens(ctx, userID, s.now().UTC())
}

// RevokeAPIToken deletes an active API token by ID for userID.
func (s *AuthService) RevokeAPIToken(ctx context.Context, userID, tokenID string) error {
	if s == nil || s.repo == nil {
		return ErrAuthStoreUnavailable
	}
	if strings.TrimSpace(tokenID) == "" {
		return ErrAuthTokenNotFound
	}

	return s.repo.RevokeAPITokenByID(ctx, userID, strings.TrimSpace(tokenID))
}

func (s *AuthService) createAdminAccount(ctx context.Context, rawUsername, password string) (repository.User, error) {
	username, err := validateCredentials(rawUsername, password)
	if err != nil {
		return repository.User{}, err
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return repository.User{}, fmt.Errorf("hash administrator password: %w", err)
	}

	userID, err := randomHexID(identifierRandomByte)
	if err != nil {
		return repository.User{}, err
	}

	now := s.now().UTC()
	user := repository.User{
		ID:           userID,
		Username:     username,
		PasswordHash: string(hashed),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.repo.CreateInitialAdmin(ctx, user); err != nil {
		return repository.User{}, err
	}

	return user, nil
}

func (s *AuthService) issueToken(
	ctx context.Context,
	user repository.User,
	name, kind string,
	ttl time.Duration,
) (IssuedAuth, error) {
	tokenID, err := randomHexID(identifierRandomByte)
	if err != nil {
		return IssuedAuth{}, err
	}

	prefix := apiTokenPrefix
	if kind == repository.TokenKindSession {
		prefix = sessionTokenPrefix
	}

	secretPart, err := randomBase64URL(tokenEntropyBytes)
	if err != nil {
		return IssuedAuth{}, err
	}
	rawToken := prefix + secretPart

	var csrfToken string
	if kind == repository.TokenKindSession {
		csrfToken, err = randomBase64URL(tokenEntropyBytes)
		if err != nil {
			return IssuedAuth{}, err
		}
	}

	now := s.now().UTC()
	expiresAt := now.Add(ttl)

	record := repository.TokenRecord{
		ID:         tokenID,
		UserID:     user.ID,
		Name:       name,
		Kind:       kind,
		TokenHash:  hashRawToken(rawToken),
		CSRFToken:  csrfToken,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
		LastUsedAt: now,
	}
	if err := s.repo.CreateToken(ctx, record); err != nil {
		return IssuedAuth{}, err
	}

	return IssuedAuth{
		TokenID:   tokenID,
		Token:     rawToken,
		CSRFToken: csrfToken,
		ExpiresAt: expiresAt,
		User: UserProfile{
			ID:        user.ID,
			Username:  user.Username,
			CreatedAt: user.CreatedAt,
		},
	}, nil
}

func (s *AuthService) verifySetupToken(candidate string) bool {
	if s.cfg.SetupToken == "" {
		return true
	}

	expected := []byte(s.cfg.SetupToken)
	actual := []byte(strings.TrimSpace(candidate))
	if len(expected) != len(actual) {
		return false
	}

	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func (s *AuthService) ttlForKind(kind string) time.Duration {
	if normalizeTokenKind(kind) == repository.TokenKindAPIToken {
		return defaultAPITokenTTL
	}

	return s.SessionTTL()
}

func normalizeTokenKind(kind string) string {
	if kind == repository.TokenKindAPIToken {
		return repository.TokenKindAPIToken
	}

	return repository.TokenKindSession
}

func normalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func validateCredentials(rawUsername, password string) (string, error) {
	username := normalizeUsername(rawUsername)
	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("%w: username must be 3 to 64 lowercase alphanumeric, '.', '_', or '-' characters", ErrInvalidAuthInput)
	}

	if len(password) < minPasswordBytes || len(password) > maxPasswordBytes {
		return "", fmt.Errorf("%w: password must be between %d and %d bytes", ErrInvalidAuthInput, minPasswordBytes, maxPasswordBytes)
	}
	if strings.TrimSpace(password) == "" || strings.ContainsRune(password, 0) {
		return "", fmt.Errorf("%w: password must contain non-whitespace characters", ErrInvalidAuthInput)
	}

	return username, nil
}

func resolveAPITokenTTL(expiresInSeconds int64) (time.Duration, error) {
	if expiresInSeconds == 0 {
		return defaultAPITokenTTL, nil
	}
	if expiresInSeconds < minAPITokenSeconds || expiresInSeconds > maxAPITokenSeconds {
		return 0, fmt.Errorf(
			"%w: expiresInSeconds must be between %d and %d",
			ErrInvalidAuthInput,
			minAPITokenSeconds,
			maxAPITokenSeconds,
		)
	}

	return time.Duration(expiresInSeconds) * time.Second, nil
}

func hasValidTokenFormat(rawToken string) bool {
	hasPrefix := strings.HasPrefix(rawToken, sessionTokenPrefix) || strings.HasPrefix(rawToken, apiTokenPrefix)
	return hasPrefix && len(rawToken) >= 40 && len(rawToken) <= 128
}

func hashRawToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

func randomHexID(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}

	return hex.EncodeToString(buf), nil
}

func randomBase64URL(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func mustGenerateDummyHash() []byte {
	digest, err := bcrypt.GenerateFromPassword([]byte("kubereel-dummy-timing-shield"), bcrypt.MinCost)
	if err != nil {
		return []byte("$2a$04$abcdefghijklmnopqrstuu5sO1x7V0yV1Z2X3Y4Z5A6B7C8D9E0F1")
	}

	return digest
}
