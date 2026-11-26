package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeS3Object struct {
	body        []byte
	contentType string
	modTime     time.Time
}

type fakeS3Server struct {
	mu           sync.RWMutex
	objects      map[string]fakeS3Object
	getCount     atomic.Int64
	putCount     atomic.Int64
	lastRangeHdr atomic.Value
	lastSecToken atomic.Value
}

func newFakeS3Server(t *testing.T) (*fakeS3Server, *httptest.Server) {
	t.Helper()
	state := &fakeS3Server{
		objects: make(map[string]fakeS3Object),
	}
	srv := httptest.NewServer(http.HandlerFunc(state.serveHTTP))
	t.Cleanup(srv.Close)
	return state, srv
}

func (f *fakeS3Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if secToken := r.Header.Get("X-Amz-Security-Token"); secToken != "" {
		f.lastSecToken.Store(secToken)
	}

	objectPath := strings.TrimPrefix(r.URL.Path, "/")
	if objectPath == "" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPut:
		f.handlePut(w, r, objectPath)
	case http.MethodHead:
		f.handleHead(w, objectPath)
	case http.MethodGet:
		f.handleGet(w, r, objectPath)
	case http.MethodDelete:
		f.handleDelete(w, objectPath)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3Server) handlePut(w http.ResponseWriter, r *http.Request, objectPath string) {
	f.putCount.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	f.mu.Lock()
	f.objects[objectPath] = fakeS3Object{
		body:        body,
		contentType: r.Header.Get("Content-Type"),
		modTime:     time.Now().UTC().Truncate(time.Second),
	}
	f.mu.Unlock()

	w.Header().Set("ETag", `"etag-1"`)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3Server) handleHead(w http.ResponseWriter, objectPath string) {
	f.mu.RLock()
	obj, ok := f.objects[objectPath]
	f.mu.RUnlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	writeObjectHeaders(w, obj)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3Server) handleGet(w http.ResponseWriter, r *http.Request, objectPath string) {
	f.getCount.Add(1)
	rangeHdr := r.Header.Get("Range")
	f.lastRangeHdr.Store(rangeHdr)

	f.mu.RLock()
	obj, ok := f.objects[objectPath]
	f.mu.RUnlock()
	if !ok {
		writeS3ErrorXML(w, http.StatusNotFound, "NoSuchKey")
		return
	}

	start, end, partial, okRange := parseByteRange(rangeHdr, len(obj.body))
	if !okRange {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	slice := obj.body[start:end]
	w.Header().Set("Content-Type", obj.contentType)
	w.Header().Set("Last-Modified", obj.modTime.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(obj.body)))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.Write(slice)
}

func (f *fakeS3Server) handleDelete(w http.ResponseWriter, objectPath string) {
	f.mu.Lock()
	delete(f.objects, objectPath)
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeS3Server) putRaw(objectPath string, body []byte, modTime time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[objectPath] = fakeS3Object{
		body:        append([]byte(nil), body...),
		contentType: "application/octet-stream",
		modTime:     modTime.UTC().Truncate(time.Second),
	}
}

func writeObjectHeaders(w http.ResponseWriter, obj fakeS3Object) {
	w.Header().Set("Content-Type", obj.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
	w.Header().Set("Last-Modified", obj.modTime.UTC().Format(http.TimeFormat))
	w.Header().Set("ETag", `"etag-1"`)
}

func writeS3ErrorXML(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code></Error>`, code)
}

func parseByteRange(header string, size int) (int, int, bool, bool) {
	if header == "" {
		return 0, size, false, true
	}
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return 0, 0, false, false
	}
	startRaw, endRaw, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, false, false
	}
	start, err := strconv.Atoi(startRaw)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, false
	}
	end, err := strconv.Atoi(endRaw)
	if err != nil || end < start {
		return 0, 0, false, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end + 1, true, true
}

func newTestS3Store(t *testing.T, maxBytes int64, retention time.Duration) (*S3, *fakeS3Server) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	fake, srv := newFakeS3Server(t)
	store, err := NewS3(context.Background(), S3Config{
		Bucket:         "kubereel-artifacts",
		Region:         "us-east-1",
		Prefix:         "prod/cluster-a",
		Endpoint:       srv.URL,
		ForcePathStyle: true,
		VirtualRoot:    "/data",
		MaxBytes:       maxBytes,
		Retention:      retention,
		HTTPClient:     srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, fake
}

func TestS3PutOpenRangeSeekDelete(t *testing.T) {
	store, fake := newTestS3Store(t, 64, 0)
	ctx := context.Background()
	key := "workflows/123/scene.svg"

	if got, err := store.Key("/data/" + key); err != nil || got != key {
		t.Fatalf("Key(/data/...) = %q, %v", got, err)
	}
	if got, err := store.Key(key); err != nil || got != key {
		t.Fatalf("Key(relative) = %q, %v", got, err)
	}

	if err := store.Put(ctx, key, strings.NewReader("first")); err != nil {
		t.Fatal(err)
	}

	object, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	info, err := object.Stat()
	if err != nil || info.Name() != "scene.svg" || info.Size() != 5 {
		t.Fatalf("Stat() = %+v, %v", info, err)
	}

	if _, err := object.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := object.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if fake.getCount.Load() != 0 {
		t.Fatalf("Seek issued unexpected GET request count = %d", fake.getCount.Load())
	}

	data, err := io.ReadAll(object)
	if err != nil || string(data) != "rst" {
		t.Fatalf("seek read = %q, %v", data, err)
	}
	if gotRange, _ := fake.lastRangeHdr.Load().(string); gotRange != "bytes=2-4" {
		t.Fatalf("Range header = %q, want bytes=2-4", gotRange)
	}

	// Verify http.ServeContent HTTP 206 Partial Content compatibility.
	req := httptest.NewRequest(http.MethodGet, "/artifact", nil)
	req.Header.Set("Range", "bytes=1-3")
	rec := httptest.NewRecorder()
	http.ServeContent(rec, req, info.Name(), info.ModTime(), object)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "irs" {
		t.Fatalf("ServeContent = %d %q", rec.Code, rec.Body.String())
	}
	if err := object.Close(); err != nil {
		t.Fatal(err)
	}

	if err := store.Put(ctx, key, strings.NewReader("second")); err != nil {
		t.Fatal(err)
	}
	if got := readArtifact(t, store, key); got != "second" {
		t.Fatalf("overwritten artifact = %q", got)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, key); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("second Delete = %v, want fs.ErrNotExist", err)
	}
	if _, err := store.Open(ctx, key); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open deleted = %v, want fs.ErrNotExist", err)
	}
}

func TestS3RejectsInvalidKeysAndConfigs(t *testing.T) {
	store, _ := newTestS3Store(t, 64, 0)
	ctx := context.Background()

	invalidKeys := []string{"", ".", "../escape", "a/../b", "/absolute", "a//b", `a\b`, "a\x00b", strings.Repeat("x", 254)}
	for _, key := range invalidKeys {
		if err := store.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
	if _, err := store.Key("/data/../outside"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Key(/data/../outside) = %v, want ErrInvalidKey", err)
	}
	if _, err := store.Key("/var/lib/secret"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Key(/var/lib/secret) = %v, want ErrInvalidKey", err)
	}

	badConfigs := []S3Config{
		{Bucket: "ab", Region: "us-east-1", MaxBytes: 10},
		{Bucket: "Invalid_Upper", Region: "us-east-1", MaxBytes: 10},
		{Bucket: "valid-bucket", Region: "", MaxBytes: 10},
		{Bucket: "valid-bucket", Region: "us-east-1", Prefix: "../escape", MaxBytes: 10},
		{Bucket: "valid-bucket", Region: "us-east-1", Endpoint: "ftp://bad", MaxBytes: 10},
		{Bucket: "valid-bucket", Region: "us-east-1", Endpoint: "https://user:pass@s3.local", MaxBytes: 10},
		{Bucket: "valid-bucket", Region: "us-east-1", MaxBytes: 0},
	}
	for _, bad := range badConfigs {
		if _, err := NewS3(ctx, bad); err == nil {
			t.Fatalf("accepted invalid S3Config: %+v", bad)
		}
	}
}

func TestS3BoundsCancellationAndClose(t *testing.T) {
	store, fake := newTestS3Store(t, 4, 0)
	ctx := context.Background()

	if err := store.Put(ctx, "item", strings.NewReader("good")); err != nil {
		t.Fatal(err)
	}
	putsBefore := fake.putCount.Load()
	if err := store.Put(ctx, "item", strings.NewReader("oversized")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized Put = %v, want ErrTooLarge", err)
	}
	if fake.putCount.Load() != putsBefore {
		t.Fatal("oversized Put unexpectedly sent network request to S3")
	}
	if got := readArtifact(t, store, "item"); got != "good" {
		t.Fatalf("oversized Put corrupted existing object: %q", got)
	}

	fake.putRaw("kubereel-artifacts/prod/cluster-a/external", []byte("oversized"), time.Now())
	if _, err := store.Open(ctx, "external"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized Open = %v, want ErrTooLarge", err)
	}

	object, err := store.Open(ctx, "item")
	if err != nil {
		t.Fatal(err)
	}
	fake.putRaw("kubereel-artifacts/prod/cluster-a/item", []byte("extended-later"), time.Now())
	grownData, readErr := io.ReadAll(object)
	if err := errors.Join(readErr, object.Close()); err != nil || len(grownData) > 4 {
		t.Fatalf("grown S3 object read = %q, %v", grownData, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Put(cancelled, "item", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Put = %v", err)
	}
	if _, err := store.Open(cancelled, "item"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Open = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "item", strings.NewReader("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Put after Close = %v, want ErrClosed", err)
	}
}

func TestS3Retention(t *testing.T) {
	store, fake := newTestS3Store(t, 32, time.Hour)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	completed := now.Add(-2 * time.Hour)
	fake.putRaw("kubereel-artifacts/prod/cluster-a/item", []byte("data"), completed.Add(-time.Minute))

	if deleted, err := store.DeleteExpired(ctx, "item", completed, completed.Add(30*time.Minute)); err != nil || deleted {
		t.Fatalf("unexpired = %v, %v", deleted, err)
	}
	if deleted, err := store.DeleteExpired(ctx, "item", completed, now); err != nil || !deleted {
		t.Fatalf("expired = %v, %v", deleted, err)
	}
	if deleted, err := store.DeleteExpired(ctx, "item", completed, now); !errors.Is(err, fs.ErrNotExist) || deleted {
		t.Fatalf("already deleted expired = %v, %v, want fs.ErrNotExist", deleted, err)
	}

	fake.putRaw("kubereel-artifacts/prod/cluster-a/newer", []byte("new"), completed.Add(time.Minute))
	if deleted, err := store.DeleteExpired(ctx, "newer", completed, now); err == nil || deleted {
		t.Fatalf("modified after completion = %v, %v", deleted, err)
	}
}

type stsInterceptTransport struct {
	s3Transport http.RoundTripper
	stsCalls    atomic.Int64
	lastToken   atomic.Value
	lastRoleARN atomic.Value
}

func (t *stsInterceptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "sts.") || strings.HasPrefix(req.URL.Host, "sts.") {
		t.stsCalls.Add(1)
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		values := parseFormEncoded(string(bodyBytes) + "&" + req.URL.RawQuery)
		t.lastToken.Store(values["WebIdentityToken"])
		t.lastRoleARN.Store(values["RoleArn"])

		exp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		xmlResp := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleWithWebIdentityResult>
    <Credentials>
      <AccessKeyId>ASIAWORKLOADIDENTITY</AccessKeyId>
      <SecretAccessKey>workload-secret-access-key</SecretAccessKey>
      <SessionToken>irsa-session-token-xyz</SessionToken>
      <Expiration>%s</Expiration>
    </Credentials>
  </AssumeRoleWithWebIdentityResult>
</AssumeRoleWithWebIdentityResponse>`, exp)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/xml"}},
			Body:       io.NopCloser(bytes.NewBufferString(xmlResp)),
			Request:    req,
		}, nil
	}

	return t.s3Transport.RoundTrip(req)
}

func parseFormEncoded(raw string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(raw, "&") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[k] = strings.ReplaceAll(strings.ReplaceAll(v, "%3A", ":"), "%2F", "/")
	}
	return out
}

func TestS3WorkloadIdentityWebIdentityToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oidc-token")
	if err := os.WriteFile(tokenPath, []byte("eyJhbGciOiJSUzI1NiJ9.eks-oidc-jwt"), 0600); err != nil {
		t.Fatal(err)
	}

	roleARN := "arn:aws:iam::123456789012:role/kubereel-worker"
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none-cfg"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", tokenPath)
	t.Setenv("AWS_ROLE_ARN", roleARN)

	fakeS3, s3Srv := newFakeS3Server(t)
	transport := &stsInterceptTransport{s3Transport: s3Srv.Client().Transport}
	httpClient := &http.Client{Transport: transport}

	store, err := NewS3(context.Background(), S3Config{
		Bucket:         "kubereel-cloud",
		Region:         "us-west-2",
		Endpoint:       s3Srv.URL,
		ForcePathStyle: true,
		MaxBytes:       1024,
		HTTPClient:     httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Put(context.Background(), "workflows/run-1/final.mp4", strings.NewReader("mp4-bytes")); err != nil {
		t.Fatal(err)
	}

	if transport.stsCalls.Load() == 0 {
		t.Fatal("expected STS AssumeRoleWithWebIdentity call for IRSA workload identity")
	}
	if gotToken, _ := transport.lastToken.Load().(string); gotToken != "eyJhbGciOiJSUzI1NiJ9.eks-oidc-jwt" {
		t.Fatalf("STS WebIdentityToken = %q", gotToken)
	}
	if gotRole, _ := transport.lastRoleARN.Load().(string); gotRole != roleARN {
		t.Fatalf("STS RoleArn = %q, want %q", gotRole, roleARN)
	}
	if gotSecToken, _ := fakeS3.lastSecToken.Load().(string); gotSecToken != "irsa-session-token-xyz" {
		t.Fatalf("S3 X-Amz-Security-Token = %q, want irsa-session-token-xyz", gotSecToken)
	}
}
