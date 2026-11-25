// Package gemini implements the official Google Gemini Interactions API text provider.
package gemini

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/observability"
	"github.com/tnqbao/kubereel/internal/provider"
)

const (
	DefaultBaseURL = "https://generativelanguage.googleapis.com"
	DefaultModel   = "gemini-4-hydrogen"

	defaultTimeout   = 60 * time.Second
	maxPromptBytes   = 32768
	maxResponseBytes = 4 << 20
)

var (
	ErrInvalidConfig      = errors.New("invalid gemini provider configuration")
	ErrInvalidRequest     = errors.New("invalid gemini request")
	ErrInvalidModel       = errors.New("unsupported or deprecated gemini model")
	ErrUnauthorized       = errors.New("gemini authentication failed")
	ErrRateLimited        = errors.New("gemini rate limit exceeded")
	ErrBlockedEndpoint    = errors.New("gemini endpoint blocked by SSRF policy")
	ErrRedirectDisallowed = errors.New("gemini HTTP redirects are disallowed")

	modelNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	cgnatPrefix      = netip.MustParsePrefix("100.64.0.0/10")
)

// Config configures an official Gemini API key text provider.
type Config struct {
	APIKey       string
	BaseURL      string
	DefaultModel string
	Timeout      time.Duration
}

// Provider executes text generation against the official Gemini Interactions API.
type Provider struct {
	apiKey       string
	endpointURL  string
	defaultModel string
	client       *http.Client
}

var _ provider.TextProvider = (*Provider)(nil)

type createInteractionRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
	Store bool   `json:"store"`
}

type interactionResponse struct {
	ID         string             `json:"id"`
	Status     string             `json:"status"`
	OutputText string             `json:"output_text"`
	Steps      []interactionStep  `json:"steps"`
	Errors     []interactionError `json:"errors"`
	Error      *googleStatusError `json:"error"`
}

type interactionStep struct {
	Type    string               `json:"type"`
	Content []interactionContent `json:"content"`
}

type interactionContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type interactionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type googleStatusError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// New validates cfg and constructs an SSRF-protected Gemini Provider.
func New(cfg Config) (*Provider, error) {
	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, fmt.Errorf("%w: timeout must not be negative", ErrInvalidConfig)
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}

	return newProvider(cfg, newSafeHTTPClient(timeout), true)
}

func newWithHTTPClient(cfg Config, client *http.Client) (*Provider, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: HTTP client is required", ErrInvalidConfig)
	}

	return newProvider(cfg, client, false)
}

func newProvider(cfg Config, client *http.Client, enforcePublicLiteralIP bool) (*Provider, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n\x00") {
		return nil, fmt.Errorf("%w: API key is required", ErrInvalidConfig)
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	endpointURL, err := buildInteractionsEndpoint(baseURL, enforcePublicLiteralIP)
	if err != nil {
		return nil, err
	}

	model := strings.TrimSpace(cfg.DefaultModel)
	if model == "" {
		model = DefaultModel
	}
	if err := ValidateModel(model); err != nil {
		return nil, err
	}

	return &Provider{
		apiKey:       apiKey,
		endpointURL:  endpointURL,
		defaultModel: model,
		client:       client,
	}, nil
}

// ValidateEndpoint checks that rawURL is a safe HTTPS base URL with no embedded credentials or query parameters.
func ValidateEndpoint(rawURL string) error {
	_, err := buildInteractionsEndpoint(rawURL, true)
	return err
}

func buildInteractionsEndpoint(rawURL string, enforcePublicLiteralIP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("%w: invalid base URL", ErrInvalidConfig)
	}

	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%w: base URL must use https with a valid host", ErrInvalidConfig)
	}

	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: base URL must not contain credentials, query parameters, or fragments", ErrInvalidConfig)
	}

	if addr, parseErr := netip.ParseAddr(parsed.Hostname()); parseErr == nil && enforcePublicLiteralIP && !isPublicAddr(addr) {
		return "", ErrBlockedEndpoint
	}

	cleanPath := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(cleanPath, "/interactions") {
		parsed.Path = cleanPath
		return parsed.String(), nil
	}
	if strings.HasSuffix(cleanPath, "/v1") || strings.HasSuffix(cleanPath, "/v1beta") {
		parsed.Path = cleanPath + "/interactions"
		return parsed.String(), nil
	}

	parsed.Path = cleanPath + "/v1/interactions"
	return parsed.String(), nil
}

// ValidateModel rejects empty, malformed, or deprecated Gemini model names.
func ValidateModel(rawModel string) error {
	model := strings.TrimSpace(rawModel)
	if !modelNamePattern.MatchString(model) {
		return fmt.Errorf("%w: %q", ErrInvalidModel, rawModel)
	}

	lower := strings.ToLower(model)
	for _, deprecatedPrefix := range []string{"gemini-1.5", "gemini-2.0", "gemini-2.5"} {
		if strings.HasPrefix(lower, deprecatedPrefix) {
			return fmt.Errorf("%w: %q is deprecated; use %s or newer", ErrInvalidModel, model, DefaultModel)
		}
	}

	return nil
}

// Generate calls the Gemini Interactions API and returns the generated text.
func (p *Provider) Generate(ctx context.Context, req provider.TextRequest) (provider.TextResult, error) {
	if err := ctx.Err(); err != nil {
		return provider.TextResult{}, err
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" || len(prompt) > maxPromptBytes {
		return provider.TextResult{}, fmt.Errorf("%w: prompt must contain 1 to %d bytes", ErrInvalidRequest, maxPromptBytes)
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.defaultModel
	}
	if err := ValidateModel(model); err != nil {
		return provider.TextResult{}, err
	}

	payload, err := json.Marshal(createInteractionRequest{
		Model: model,
		Input: prompt,
		Store: false,
	})
	if err != nil {
		return provider.TextResult{}, fmt.Errorf("encode gemini request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpointURL, bytes.NewReader(payload))
	if err != nil {
		return provider.TextResult{}, fmt.Errorf("create gemini request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.apiKey)

	resp, err := observability.DoProviderRequest(p.client, httpReq, "gemini")
	if err != nil {
		return provider.TextResult{}, p.classifyTransportError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return provider.TextResult{}, fmt.Errorf("%w: read gemini response: %v", provider.ErrSubmissionUncertain, scrubSecret(err.Error(), p.apiKey))
	}
	if len(body) > maxResponseBytes {
		return provider.TextResult{}, fmt.Errorf("%w: gemini response exceeded %d bytes", provider.ErrSubmissionUncertain, maxResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return provider.TextResult{}, p.classifyHTTPError(resp.StatusCode, body)
	}

	return parseInteractionText(body, p.apiKey)
}

func (p *Provider) classifyTransportError(err error) error {
	if errors.Is(err, ErrBlockedEndpoint) {
		return ErrBlockedEndpoint
	}
	if errors.Is(err, ErrRedirectDisallowed) {
		return ErrRedirectDisallowed
	}

	sanitized := scrubSecret(err.Error(), p.apiKey)
	return fmt.Errorf("%w: %s", provider.ErrSubmissionUncertain, sanitized)
}

func (p *Provider) classifyHTTPError(statusCode int, body []byte) error {
	detail := extractErrorDetail(body, p.apiKey)

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, statusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, detail)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w (status %d): %s", ErrInvalidRequest, statusCode, detail)
	default:
		return fmt.Errorf("%w: upstream status %d: %s", provider.ErrSubmissionUncertain, statusCode, detail)
	}
}

func parseInteractionText(body []byte, apiKey string) (provider.TextResult, error) {
	var parsed interactionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return provider.TextResult{}, fmt.Errorf("decode gemini response: %w", err)
	}

	if parsed.Status == "failed" || parsed.Status == "cancelled" {
		reason := "interaction " + parsed.Status
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			reason = scrubSecret(parsed.Errors[0].Message, apiKey)
		}
		return provider.TextResult{}, fmt.Errorf("gemini %s", reason)
	}

	text := extractTrailingModelText(parsed.Steps)
	if text == "" {
		text = strings.TrimSpace(parsed.OutputText)
	}
	if text == "" {
		return provider.TextResult{}, fmt.Errorf("gemini interaction returned empty text output")
	}

	return provider.TextResult{Text: text}, nil
}

func extractTrailingModelText(steps []interactionStep) string {
	var stepTexts []string

	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.Type != "model_output" {
			if len(stepTexts) > 0 {
				break
			}
			continue
		}

		partText := joinStepTextParts(step.Content)
		if partText != "" {
			stepTexts = append(stepTexts, partText)
		}
	}

	var combined strings.Builder
	for i := len(stepTexts) - 1; i >= 0; i-- {
		combined.WriteString(stepTexts[i])
	}

	return strings.TrimSpace(combined.String())
}

func joinStepTextParts(parts []interactionContent) string {
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}

	return builder.String()
}

func extractErrorDetail(body []byte, apiKey string) string {
	var parsed interactionResponse
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return scrubSecret(parsed.Error.Message, apiKey)
		}
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			return scrubSecret(parsed.Errors[0].Message, apiKey)
		}
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "request failed"
	}
	if len(trimmed) > 256 {
		trimmed = trimmed[:256]
	}

	return scrubSecret(trimmed, apiKey)
}

func scrubSecret(message, secret string) string {
	if secret == "" {
		return message
	}

	return strings.ReplaceAll(message, secret, "[REDACTED]")
}

func newSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialPublicAddress(ctx, dialer, network, addr)
		},
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return ErrRedirectDisallowed
		},
	}
}

func dialPublicAddress(ctx context.Context, dialer *net.Dialer, network, hostPort string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, err
	}

	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no IP addresses resolved for %s", host)
	}

	for _, addr := range addrs {
		if !isPublicAddr(addr.Unmap()) {
			return nil, ErrBlockedEndpoint
		}
	}

	return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
}

func isPublicAddr(addr netip.Addr) bool {
	unmapped := addr.Unmap()
	if !unmapped.IsValid() {
		return false
	}

	if unmapped.IsLoopback() || unmapped.IsPrivate() || unmapped.IsUnspecified() {
		return false
	}

	if unmapped.IsLinkLocalUnicast() || unmapped.IsLinkLocalMulticast() || unmapped.IsMulticast() {
		return false
	}

	if cgnatPrefix.Contains(unmapped) {
		return false
	}

	return true
}
