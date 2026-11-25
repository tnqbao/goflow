// Package fal implements the official fal.ai asynchronous Queue API for image and video generation.
package fal

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/provider"
)

const (
	DefaultQueueBaseURL = "https://queue.fal.run"

	defaultTimeout     = 60 * time.Second
	defaultMaxRetries  = 2
	maxAllowedRetries  = 5
	defaultRetryDelay  = 200 * time.Millisecond
	maxRetryAfterDelay = 10 * time.Second

	maxPromptBytes     = 16384
	maxInputImageBytes = 10 << 20
	maxJSONBytes       = 4 << 20
)

var (
	ErrInvalidConfig      = errors.New("invalid fal provider configuration")
	ErrInvalidRequest     = errors.New("invalid fal request")
	ErrInvalidModel       = errors.New("invalid fal model identifier")
	ErrInvalidHandle      = errors.New("invalid fal generation handle")
	ErrUnauthorized       = errors.New("fal authentication failed")
	ErrRateLimited        = errors.New("fal rate limit exceeded")
	ErrNotReady           = errors.New("fal generation is not completed")
	ErrGenerationFailed   = errors.New("fal generation failed")
	ErrBlockedEndpoint    = errors.New("fal endpoint blocked by SSRF policy")
	ErrRedirectDisallowed = errors.New("fal HTTP redirects are disallowed")

	modelSegmentPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	requestIDPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	paramKeyPattern     = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)
	cgnatPrefix         = netip.MustParsePrefix("100.64.0.0/10")

	reservedParamKeys = []string{"prompt", "image_url", "fal_webhook"}
)

// Config configures a fal.ai Queue API client.
type Config struct {
	APIKey         string
	BaseURL        string
	DefaultModel   string
	Timeout        time.Duration
	MaxRetries     int
	BaseRetryDelay time.Duration
	Parameters     map[string]any
}

// Provider manages authenticated requests to the fal.ai Queue API.
type Provider struct {
	apiKey         string
	baseURL        *url.URL
	defaultModel   string
	maxRetries     int
	baseRetryDelay time.Duration
	parameters     map[string]any
	client         *http.Client
	enforcePublic  bool
}

// Image adapts Provider to provider.ImageProvider and provider.Canceller.
type Image struct {
	provider *Provider
}

// Video adapts Provider to provider.VideoProvider and provider.Canceller.
type Video struct {
	provider *Provider
}

var (
	_ provider.ImageProvider = (*Image)(nil)
	_ provider.VideoProvider = (*Video)(nil)
	_ provider.Canceller     = (*Image)(nil)
	_ provider.Canceller     = (*Video)(nil)
)

type queueSubmitResponse struct {
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
	StatusURL   string `json:"status_url"`
	ResponseURL string `json:"response_url"`
	CancelURL   string `json:"cancel_url"`
}

type queueStatusResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Detail any    `json:"detail"`
}

type mediaFileRef struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

type imageResultEnvelope struct {
	Images []mediaFileRef `json:"images"`
	Image  *mediaFileRef  `json:"image"`
}

type videoResultEnvelope struct {
	Video *mediaFileRef `json:"video"`
}

// New validates cfg and returns an SSRF-protected fal.ai Provider.
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

func newProvider(cfg Config, client *http.Client, enforcePublic bool) (*Provider, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n\x00") {
		return nil, fmt.Errorf("%w: API key is required", ErrInvalidConfig)
	}

	rawBase := strings.TrimSpace(cfg.BaseURL)
	if rawBase == "" {
		rawBase = DefaultQueueBaseURL
	}

	parsedBase, err := parseAndValidateURL(rawBase, enforcePublic)
	if err != nil {
		return nil, err
	}
	parsedBase.Path = strings.TrimRight(parsedBase.Path, "/")

	model := strings.TrimSpace(cfg.DefaultModel)
	if err := validateOptionalModel(model); err != nil {
		return nil, err
	}

	retries := cfg.MaxRetries
	if retries < 0 || retries > maxAllowedRetries {
		return nil, fmt.Errorf("%w: maxRetries must be between 0 and %d", ErrInvalidConfig, maxAllowedRetries)
	}
	if cfg.MaxRetries == 0 {
		retries = defaultMaxRetries
	}

	retryDelay := cfg.BaseRetryDelay
	if retryDelay < 0 {
		return nil, fmt.Errorf("%w: baseRetryDelay must not be negative", ErrInvalidConfig)
	}
	if retryDelay == 0 {
		retryDelay = defaultRetryDelay
	}

	clonedParams, err := validateAndCloneParameters(cfg.Parameters)
	if err != nil {
		return nil, err
	}

	return &Provider{
		apiKey:         apiKey,
		baseURL:        parsedBase,
		defaultModel:   model,
		maxRetries:     retries,
		baseRetryDelay: retryDelay,
		parameters:     clonedParams,
		client:         client,
		enforcePublic:  enforcePublic,
	}, nil
}

// Image returns the image generation capability adapter.
func (p *Provider) Image() *Image {
	return &Image{provider: p}
}

// Video returns the video generation capability adapter.
func (p *Provider) Video() *Video {
	return &Video{provider: p}
}

// ValidateEndpoint checks that rawURL is a safe HTTPS base URL.
func ValidateEndpoint(rawURL string) error {
	_, err := parseAndValidateURL(rawURL, true)
	return err
}

func parseAndValidateURL(rawURL string, enforcePublic bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid URL", ErrInvalidConfig)
	}

	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("%w: URL must use https with a valid host", ErrInvalidConfig)
	}

	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: URL must not contain credentials or fragments", ErrInvalidConfig)
	}

	if addr, addrErr := netip.ParseAddr(parsed.Hostname()); addrErr == nil && enforcePublic && !isPublicAddr(addr) {
		return nil, ErrBlockedEndpoint
	}

	return parsed, nil
}

// ValidateModel verifies that rawModel is a safe {owner}/{app}[/{subpath}...] identifier.
func ValidateModel(rawModel string) error {
	model := strings.TrimSpace(rawModel)
	if model == "" || len(model) > 253 {
		return fmt.Errorf("%w: %q", ErrInvalidModel, rawModel)
	}

	segments := strings.Split(model, "/")
	if len(segments) < 2 || len(segments) > 6 {
		return fmt.Errorf("%w: %q must have 2 to 6 path segments", ErrInvalidModel, rawModel)
	}

	for _, seg := range segments {
		if seg == "." || seg == ".." || !modelSegmentPattern.MatchString(seg) {
			return fmt.Errorf("%w: invalid segment %q", ErrInvalidModel, seg)
		}
	}

	return nil
}

func validateOptionalModel(model string) error {
	if model == "" {
		return nil
	}

	return ValidateModel(model)
}

func validateAndCloneParameters(params map[string]any) (map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	if len(params) > 32 {
		return nil, fmt.Errorf("%w: at most 32 model parameters are allowed", ErrInvalidConfig)
	}

	cloned := make(map[string]any, len(params))
	for key, val := range params {
		if !paramKeyPattern.MatchString(key) || slices.Contains(reservedParamKeys, key) {
			return nil, fmt.Errorf("%w: disallowed parameter key %q", ErrInvalidConfig, key)
		}
		if !isSafeParameterValue(val) {
			return nil, fmt.Errorf("%w: unsupported value type for parameter %q", ErrInvalidConfig, key)
		}
		cloned[key] = val
	}

	return cloned, nil
}

func isSafeParameterValue(val any) bool {
	switch typed := val.(type) {
	case string:
		return len(typed) <= 1024 && !strings.ContainsRune(typed, 0)
	case bool, int, int64, float64:
		return true
	default:
		return false
	}
}

// Submit enqueues a text-to-image request on fal.ai.
func (i *Image) Submit(ctx context.Context, req provider.ImageRequest) (provider.GenerationHandle, error) {
	model, prompt, err := i.provider.resolveModelAndPrompt(req.Model, req.Prompt)
	if err != nil {
		return provider.GenerationHandle{}, err
	}

	payload := i.provider.buildInputPayload(prompt, "")
	return i.provider.submitQueue(ctx, model, payload)
}

// GetStatus polls the fal.ai queue status for an image request.
func (i *Image) GetStatus(ctx context.Context, handle provider.GenerationHandle) (provider.GenerationStatus, error) {
	return i.provider.pollQueueStatus(ctx, handle)
}

// Cancel requests cancellation of a queued or running image request.
func (i *Image) Cancel(ctx context.Context, handle provider.GenerationHandle) error {
	return i.provider.cancelQueue(ctx, handle)
}

// GetResult fetches the completed image metadata and streams the image artifact.
func (i *Image) GetResult(ctx context.Context, handle provider.GenerationHandle) (provider.MediaResult, error) {
	rawJSON, err := i.provider.fetchQueueResponseJSON(ctx, handle)
	if err != nil {
		return provider.MediaResult{}, err
	}

	var parsed imageResultEnvelope
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		return provider.MediaResult{}, fmt.Errorf("decode fal image result: %w", err)
	}

	mediaRef := selectImageMediaRef(parsed)
	if mediaRef.URL == "" {
		return provider.MediaResult{}, fmt.Errorf("fal image response contained no image URL")
	}

	return i.provider.downloadMedia(ctx, mediaRef, "image/")
}

func selectImageMediaRef(envelope imageResultEnvelope) mediaFileRef {
	if len(envelope.Images) > 0 && envelope.Images[0].URL != "" {
		return envelope.Images[0]
	}
	if envelope.Image != nil {
		return *envelope.Image
	}

	return mediaFileRef{}
}

// Submit enqueues a text-to-video or image-to-video request on fal.ai.
func (v *Video) Submit(ctx context.Context, req provider.VideoRequest) (provider.GenerationHandle, error) {
	model, prompt, err := v.provider.resolveModelAndPrompt(req.Model, req.Prompt)
	if err != nil {
		return provider.GenerationHandle{}, err
	}

	var imageDataURI string
	if req.Image != nil {
		imageDataURI, err = encodeImageDataURI(req.Image)
		if err != nil {
			return provider.GenerationHandle{}, err
		}
	}

	payload := v.provider.buildInputPayload(prompt, imageDataURI)
	return v.provider.submitQueue(ctx, model, payload)
}

// GetStatus polls the fal.ai queue status for a video request.
func (v *Video) GetStatus(ctx context.Context, handle provider.GenerationHandle) (provider.GenerationStatus, error) {
	return v.provider.pollQueueStatus(ctx, handle)
}

// Cancel requests cancellation of a queued or running video request.
func (v *Video) Cancel(ctx context.Context, handle provider.GenerationHandle) error {
	return v.provider.cancelQueue(ctx, handle)
}

// GetResult fetches the completed video metadata and streams the video artifact.
func (v *Video) GetResult(ctx context.Context, handle provider.GenerationHandle) (provider.MediaResult, error) {
	rawJSON, err := v.provider.fetchQueueResponseJSON(ctx, handle)
	if err != nil {
		return provider.MediaResult{}, err
	}

	var parsed videoResultEnvelope
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		return provider.MediaResult{}, fmt.Errorf("decode fal video result: %w", err)
	}

	if parsed.Video == nil || parsed.Video.URL == "" {
		return provider.MediaResult{}, fmt.Errorf("fal video response contained no video URL")
	}

	return v.provider.downloadMedia(ctx, *parsed.Video, "video/")
}

func encodeImageDataURI(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxInputImageBytes+1))
	if err != nil {
		return "", fmt.Errorf("read input image: %w", err)
	}
	if len(data) == 0 || len(data) > maxInputImageBytes {
		return "", fmt.Errorf("%w: input image must be between 1 and %d bytes", ErrInvalidRequest, maxInputImageBytes)
	}

	mimeType := detectRasterImageMIME(data)
	if mimeType == "" {
		return "", fmt.Errorf("%w: input image must be PNG, JPEG, or WebP", ErrInvalidRequest)
	}

	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func detectRasterImageMIME(data []byte) string {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp"
	}

	return ""
}

func (p *Provider) resolveModelAndPrompt(rawModel, rawPrompt string) (string, string, error) {
	model := strings.TrimSpace(rawModel)
	if model == "" {
		model = p.defaultModel
	}
	if err := ValidateModel(model); err != nil {
		return "", "", err
	}

	prompt := strings.TrimSpace(rawPrompt)
	if prompt == "" || len(prompt) > maxPromptBytes {
		return "", "", fmt.Errorf("%w: prompt must contain 1 to %d bytes", ErrInvalidRequest, maxPromptBytes)
	}

	return model, prompt, nil
}

func (p *Provider) buildInputPayload(prompt, imageDataURI string) map[string]any {
	payload := make(map[string]any, len(p.parameters)+2)
	for key, val := range p.parameters {
		payload[key] = val
	}
	payload["prompt"] = prompt
	if imageDataURI != "" {
		payload["image_url"] = imageDataURI
	}

	return payload
}

func (p *Provider) submitQueue(ctx context.Context, model string, payload map[string]any) (provider.GenerationHandle, error) {
	if err := ctx.Err(); err != nil {
		return provider.GenerationHandle{}, err
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return provider.GenerationHandle{}, fmt.Errorf("encode fal request: %w", err)
	}

	submitURL := p.endpointForPath(model)
	body, err := p.doJSONWithRetry(ctx, http.MethodPost, submitURL, encoded, false)
	if err != nil {
		return provider.GenerationHandle{}, err
	}

	var queued queueSubmitResponse
	if err := json.Unmarshal(body, &queued); err != nil {
		return provider.GenerationHandle{}, fmt.Errorf("decode fal queue response: %w", err)
	}

	appPrefix, requestID, err := extractQueueIdentity(model, queued)
	if err != nil {
		return provider.GenerationHandle{}, err
	}

	return provider.GenerationHandle{ID: appPrefix + ":" + requestID}, nil
}

func extractQueueIdentity(model string, queued queueSubmitResponse) (string, string, error) {
	reqID := strings.TrimSpace(queued.RequestID)
	if !requestIDPattern.MatchString(reqID) {
		return "", "", fmt.Errorf("fal queue response returned invalid request_id")
	}

	if prefix := parseAppPrefixFromURL(queued.ResponseURL, reqID); prefix != "" {
		return prefix, reqID, nil
	}

	segments := strings.Split(model, "/")
	return segments[0] + "/" + segments[1], reqID, nil
}

func parseAppPrefixFromURL(rawResponseURL, requestID string) string {
	if rawResponseURL == "" {
		return ""
	}

	parsed, err := url.Parse(rawResponseURL)
	if err != nil {
		return ""
	}

	trimmed := strings.Trim(parsed.Path, "/")
	suffix := "/requests/" + requestID
	if !strings.HasSuffix(trimmed, suffix) {
		return ""
	}

	prefix := strings.TrimSuffix(trimmed, suffix)
	if ValidateModel(prefix) != nil {
		return ""
	}

	return prefix
}

func parseGenerationHandle(handle provider.GenerationHandle) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(handle.ID), ":")
	if len(parts) != 2 {
		return "", "", ErrInvalidHandle
	}

	appPrefix, reqID := parts[0], parts[1]
	if ValidateModel(appPrefix) != nil || !requestIDPattern.MatchString(reqID) {
		return "", "", ErrInvalidHandle
	}

	return appPrefix, reqID, nil
}

func (p *Provider) pollQueueStatus(ctx context.Context, handle provider.GenerationHandle) (provider.GenerationStatus, error) {
	appPrefix, reqID, err := parseGenerationHandle(handle)
	if err != nil {
		return provider.GenerationStatus{}, err
	}

	statusURL := p.endpointForPath(appPrefix + "/requests/" + reqID + "/status")
	body, err := p.doJSONWithRetry(ctx, http.MethodGet, statusURL, nil, true)
	if err != nil {
		return provider.GenerationStatus{}, err
	}

	var parsed queueStatusResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return provider.GenerationStatus{}, fmt.Errorf("decode fal status: %w", err)
	}

	return mapQueueStatus(parsed, p.apiKey)
}

func mapQueueStatus(parsed queueStatusResponse, apiKey string) (provider.GenerationStatus, error) {
	switch strings.ToUpper(strings.TrimSpace(parsed.Status)) {
	case "IN_QUEUE":
		return provider.GenerationStatus{State: provider.GenerationPending}, nil
	case "IN_PROGRESS":
		return provider.GenerationStatus{State: provider.GenerationRunning}, nil
	case "COMPLETED":
		if strings.TrimSpace(parsed.Error) != "" {
			return provider.GenerationStatus{
				State:   provider.GenerationFailed,
				Failure: scrubSecret(parsed.Error, apiKey),
			}, nil
		}
		return provider.GenerationStatus{State: provider.GenerationSucceeded}, nil
	case "CANCELLED", "CANCELED":
		return provider.GenerationStatus{State: provider.GenerationCancelled}, nil
	case "FAILED", "ERROR":
		failure := scrubSecret(strings.TrimSpace(parsed.Error), apiKey)
		if failure == "" {
			failure = "fal generation failed"
		}
		return provider.GenerationStatus{
			State:   provider.GenerationFailed,
			Failure: failure,
		}, nil
	default:
		return provider.GenerationStatus{}, fmt.Errorf("unknown fal queue status %q", parsed.Status)
	}
}

func (p *Provider) cancelQueue(ctx context.Context, handle provider.GenerationHandle) error {
	appPrefix, reqID, err := parseGenerationHandle(handle)
	if err != nil {
		return err
	}

	cancelURL := p.endpointForPath(appPrefix + "/requests/" + reqID + "/cancel")
	_, err = p.doJSONWithRetry(ctx, http.MethodPut, cancelURL, nil, true)
	return err
}

func (p *Provider) fetchQueueResponseJSON(ctx context.Context, handle provider.GenerationHandle) ([]byte, error) {
	status, err := p.pollQueueStatus(ctx, handle)
	if err != nil {
		return nil, err
	}

	switch status.State {
	case provider.GenerationSucceeded:
	case provider.GenerationFailed:
		return nil, fmt.Errorf("%w: %s", ErrGenerationFailed, status.Failure)
	case provider.GenerationCancelled:
		return nil, context.Canceled
	default:
		return nil, ErrNotReady
	}

	appPrefix, reqID, err := parseGenerationHandle(handle)
	if err != nil {
		return nil, err
	}

	responseURL := p.endpointForPath(appPrefix + "/requests/" + reqID)
	return p.doJSONWithRetry(ctx, http.MethodGet, responseURL, nil, true)
}

func (p *Provider) downloadMedia(ctx context.Context, ref mediaFileRef, expectedPrefix string) (provider.MediaResult, error) {
	parsedURL, err := parseAndValidateURL(ref.URL, p.enforcePublic)
	if err != nil {
		return provider.MediaResult{}, fmt.Errorf("invalid fal media URL: %w", err)
	}

	for attempt := 0; ; attempt++ {
		media, retryAfter, retryable, attemptErr := p.downloadMediaAttempt(ctx, parsedURL, ref.ContentType, expectedPrefix)
		if attemptErr == nil {
			return media, nil
		}
		if !retryable || attempt >= p.maxRetries {
			return provider.MediaResult{}, attemptErr
		}

		if sleepErr := sleepWithBackoff(ctx, p.baseRetryDelay, attempt, retryAfter); sleepErr != nil {
			return provider.MediaResult{}, sleepErr
		}
	}
}

func (p *Provider) downloadMediaAttempt(
	ctx context.Context,
	parsedURL *url.URL,
	declaredContentType, expectedPrefix string,
) (provider.MediaResult, time.Duration, bool, error) {
	if err := ctx.Err(); err != nil {
		return provider.MediaResult{}, 0, false, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return provider.MediaResult{}, 0, false, err
	}

	// Never leak the fal API key to third-party or CDN media hosts.
	if strings.EqualFold(parsedURL.Host, p.baseURL.Host) {
		httpReq.Header.Set("Authorization", "Key "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return provider.MediaResult{}, 0, shouldRetryError(err), p.sanitizeTransportError(err, true)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		retryable := shouldRetryStatus(resp.StatusCode, true)
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return provider.MediaResult{}, retryAfter, retryable, p.classifyHTTPError(resp.StatusCode, errBody, true)
	}

	contentType := strings.TrimSpace(declaredContentType)
	if contentType == "" {
		contentType = strings.TrimSpace(resp.Header.Get("Content-Type"))
	}
	if !strings.HasPrefix(strings.ToLower(contentType), expectedPrefix) {
		_ = resp.Body.Close()
		return provider.MediaResult{}, 0, false, fmt.Errorf("unexpected fal media content type %q", contentType)
	}

	return provider.MediaResult{
		Body:        resp.Body,
		ContentType: contentType,
	}, 0, false, nil
}

func (p *Provider) doJSONWithRetry(
	ctx context.Context,
	method, targetURL string,
	payload []byte,
	idempotent bool,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for attempt := 0; ; attempt++ {
		body, retryAfter, retryable, err := p.doSingleJSONRequest(ctx, method, targetURL, payload, idempotent)
		if err == nil {
			return body, nil
		}
		if !retryable || attempt >= p.maxRetries {
			return nil, err
		}

		if sleepErr := sleepWithBackoff(ctx, p.baseRetryDelay, attempt, retryAfter); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

func (p *Provider) doSingleJSONRequest(
	ctx context.Context,
	method, targetURL string,
	payload []byte,
	idempotent bool,
) ([]byte, time.Duration, bool, error) {
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return nil, 0, false, fmt.Errorf("create fal request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Key "+p.apiKey)
	httpReq.Header.Set("Accept", "application/json")
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		retryable := idempotent && shouldRetryError(err)
		return nil, 0, retryable, p.sanitizeTransportError(err, idempotent)
	}
	defer resp.Body.Close()

	rawBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes+1))
	if readErr != nil {
		return nil, 0, idempotent, p.wrapReadError(readErr, idempotent)
	}
	if len(rawBody) > maxJSONBytes {
		return nil, 0, false, fmt.Errorf("fal response exceeded %d bytes", maxJSONBytes)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return rawBody, 0, false, nil
	}

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	retryable := shouldRetryStatus(resp.StatusCode, idempotent)

	return nil, retryAfter, retryable, p.classifyHTTPError(resp.StatusCode, rawBody, idempotent)
}

func shouldRetryStatus(statusCode int, idempotent bool) bool {
	if statusCode == http.StatusTooManyRequests {
		return true
	}

	if !idempotent {
		return false
	}

	return statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout
}

func shouldRetryError(err error) bool {
	if errors.Is(err, ErrBlockedEndpoint) || errors.Is(err, ErrRedirectDisallowed) {
		return false
	}

	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func (p *Provider) sanitizeTransportError(err error, idempotent bool) error {
	if errors.Is(err, ErrBlockedEndpoint) {
		return ErrBlockedEndpoint
	}
	if errors.Is(err, ErrRedirectDisallowed) {
		return ErrRedirectDisallowed
	}

	sanitized := scrubSecret(err.Error(), p.apiKey)
	if !idempotent {
		return fmt.Errorf("%w: %s", provider.ErrSubmissionUncertain, sanitized)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return fmt.Errorf("fal transport error: %s", sanitized)
}

func (p *Provider) wrapReadError(err error, idempotent bool) error {
	sanitized := scrubSecret(err.Error(), p.apiKey)
	if !idempotent {
		return fmt.Errorf("%w: %s", provider.ErrSubmissionUncertain, sanitized)
	}

	return fmt.Errorf("read fal response: %s", sanitized)
}

func (p *Provider) classifyHTTPError(statusCode int, body []byte, idempotent bool) error {
	detail := extractErrorDetail(body, p.apiKey)

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrUnauthorized, statusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, detail)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w (status %d): %s", ErrInvalidRequest, statusCode, detail)
	default:
		if !idempotent {
			return fmt.Errorf("%w: upstream status %d: %s", provider.ErrSubmissionUncertain, statusCode, detail)
		}
		return fmt.Errorf("fal upstream status %d: %s", statusCode, detail)
	}
}

func extractErrorDetail(body []byte, apiKey string) string {
	if jsonDetail := extractJSONErrorMessage(body); jsonDetail != "" {
		return scrubSecret(jsonDetail, apiKey)
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

func extractJSONErrorMessage(body []byte) string {
	var parsed struct {
		Detail any    `json:"detail"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if text, ok := parsed.Detail.(string); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}

	return strings.TrimSpace(parsed.Error)
}

func parseRetryAfter(headerValue string) time.Duration {
	trimmed := strings.TrimSpace(headerValue)
	if trimmed == "" {
		return 0
	}

	seconds, err := strconv.Atoi(trimmed)
	if err != nil || seconds <= 0 {
		return 0
	}

	delay := time.Duration(seconds) * time.Second
	if delay > maxRetryAfterDelay {
		return maxRetryAfterDelay
	}

	return delay
}

func sleepWithBackoff(ctx context.Context, base time.Duration, attempt int, retryAfter time.Duration) error {
	delay := base * time.Duration(1<<attempt)
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > maxRetryAfterDelay {
		delay = maxRetryAfterDelay
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (p *Provider) endpointForPath(relPath string) string {
	joined := strings.TrimRight(p.baseURL.Path, "/") + "/" + strings.TrimLeft(relPath, "/")
	clone := *p.baseURL
	clone.Path = joined
	return clone.String()
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

	return !cgnatPrefix.Contains(unmapped)
}
