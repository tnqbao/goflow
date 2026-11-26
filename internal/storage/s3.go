package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	defaultVirtualRoot = "/data"
	maxS3PrefixLength  = 128
)

// S3Config configures S3-compatible object storage for artifacts.
type S3Config struct {
	Bucket         string
	Region         string
	Prefix         string
	Endpoint       string
	ForcePathStyle bool
	VirtualRoot    string
	MaxBytes       int64
	Retention      time.Duration
	HTTPClient     *http.Client
}

type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// S3 stores artifacts in AWS S3 or an S3-compatible object store.
type S3 struct {
	client      s3API
	bucket      string
	prefix      string
	virtualRoot string
	maxBytes    int64
	retention   time.Duration

	mu     sync.RWMutex
	closed bool
}

// NewS3 validates configuration, resolves ambient workload identity or AWS credentials, and constructs an S3 store.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalizedPrefix, virtualRoot, err := validateS3StoreConfig(cfg)
	if err != nil {
		return nil, err
	}

	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.ForcePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})

	return &S3{
		client:      client,
		bucket:      cfg.Bucket,
		prefix:      normalizedPrefix,
		virtualRoot: virtualRoot,
		maxBytes:    cfg.MaxBytes,
		retention:   cfg.Retention,
	}, nil
}

func validateS3StoreConfig(cfg S3Config) (string, string, error) {
	if cfg.MaxBytes <= 0 {
		return "", "", fmt.Errorf("artifact max bytes must be positive")
	}
	if cfg.Retention < 0 {
		return "", "", fmt.Errorf("artifact retention must not be negative")
	}
	if err := ValidateS3Bucket(cfg.Bucket); err != nil {
		return "", "", err
	}
	if err := ValidateS3Region(cfg.Region); err != nil {
		return "", "", err
	}
	if err := ValidateS3Endpoint(cfg.Endpoint); err != nil {
		return "", "", err
	}

	normalizedPrefix, err := NormalizeS3Prefix(cfg.Prefix)
	if err != nil {
		return "", "", err
	}

	virtualRoot := filepath.Clean(cfg.VirtualRoot)
	if cfg.VirtualRoot == "" || virtualRoot == "." {
		virtualRoot = defaultVirtualRoot
	}

	return normalizedPrefix, virtualRoot, nil
}

func loadAWSConfig(ctx context.Context, cfg S3Config) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, awsconfig.WithHTTPClient(cfg.HTTPClient))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load S3 AWS configuration: %w", err)
	}

	return awsCfg, nil
}

// ValidateS3Bucket checks S3 bucket naming rules.
func ValidateS3Bucket(bucket string) error {
	if len(bucket) < 3 || len(bucket) > 63 {
		return fmt.Errorf("invalid S3 bucket %q: length must be between 3 and 63", bucket)
	}
	if !isLowerAlnum(bucket[0]) || !isLowerAlnum(bucket[len(bucket)-1]) {
		return fmt.Errorf("invalid S3 bucket %q: must start and end with a lowercase letter or digit", bucket)
	}

	for i := 0; i < len(bucket); i++ {
		ch := bucket[i]
		if !isLowerAlnum(ch) && ch != '-' && ch != '.' {
			return fmt.Errorf("invalid S3 bucket %q: unexpected character %q", bucket, ch)
		}
	}

	return nil
}

// ValidateS3Region checks that an S3 region identifier is well-formed.
func ValidateS3Region(region string) error {
	if len(region) == 0 || len(region) > 64 {
		return fmt.Errorf("invalid S3 region %q", region)
	}

	for i := 0; i < len(region); i++ {
		ch := region[i]
		isLetter := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
		isDigit := ch >= '0' && ch <= '9'
		if !isLetter && !isDigit && ch != '-' && ch != '_' {
			return fmt.Errorf("invalid S3 region %q", region)
		}
	}

	return nil
}

// ValidateS3Endpoint checks an optional custom S3-compatible base URL.
func ValidateS3Endpoint(rawURL string) error {
	if rawURL == "" {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid S3 endpoint %q: %w", rawURL, err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("invalid S3 endpoint %q: scheme must be http or https", rawURL)
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid S3 endpoint %q", rawURL)
	}

	return nil
}

// NormalizeS3Prefix validates and normalizes an optional S3 key prefix.
func NormalizeS3Prefix(prefix string) (string, error) {
	if prefix == "" {
		return "", nil
	}

	trimmed := strings.TrimSuffix(strings.TrimPrefix(prefix, "/"), "/")
	if trimmed == "" || len(trimmed) > maxS3PrefixLength {
		return "", fmt.Errorf("invalid S3 prefix %q", prefix)
	}
	if err := validateKey(trimmed); err != nil {
		return "", fmt.Errorf("invalid S3 prefix %q: %w", prefix, err)
	}

	return trimmed, nil
}

func isLowerAlnum(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
}

// Close marks the S3 store closed so subsequent operations fail deterministically.
func (s *S3) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Key converts a CLI path under the virtual data root (or an already relative key) into a validated storage key.
func (s *S3) Key(targetPath string) (string, error) {
	if validateKey(targetPath) == nil {
		return targetPath, nil
	}

	relativePath, err := filepath.Rel(s.virtualRoot, filepath.Clean(targetPath))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}

	key := filepath.ToSlash(relativePath)
	if err := validateKey(key); err != nil {
		return "", err
	}

	return key, nil
}

// Put streams source into a bounded temporary file to enforce maxBytes atomically before publishing to S3.
func (s *S3) Put(ctx context.Context, key string, source io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("artifact reader is nil")
	}

	fullKey, err := s.beginOperation(key)
	if err != nil {
		return err
	}

	tempFile, written, err := spoolBoundedArtifact(ctx, source, s.maxBytes)
	if err != nil {
		return err
	}
	defer removeTempFile(tempFile)

	contentType, err := sniffTempContentType(key, tempFile)
	if err != nil {
		return err
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(fullKey),
		Body:          tempFile,
		ContentLength: aws.Int64(written),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("upload S3 artifact %q: %w", key, err)
	}

	return nil
}

// Open inspects object metadata and returns a seekable HTTP Range reader bounded by maxBytes.
func (s *S3) Open(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fullKey, err := s.beginOperation(key)
	if err != nil {
		return nil, err
	}

	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return nil, mapS3Error("open", key, err)
	}

	size := aws.ToInt64(head.ContentLength)
	if size < 0 || size > s.maxBytes {
		return nil, ErrTooLarge
	}

	return &s3Object{
		ctx:     ctx,
		client:  s.client,
		bucket:  s.bucket,
		fullKey: fullKey,
		name:    path.Base(key),
		size:    size,
		modTime: aws.ToTime(head.LastModified).UTC(),
	}, nil
}

// Delete removes an existing artifact from S3, returning fs.ErrNotExist when absent.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	fullKey, err := s.beginOperation(key)
	if err != nil {
		return err
	}

	_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return mapS3Error("delete", key, err)
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return mapS3Error("delete", key, err)
	}

	return nil
}

// DeleteExpired removes an artifact once its retention window elapses without newer modifications.
func (s *S3) DeleteExpired(ctx context.Context, key string, completedAt, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if completedAt.IsZero() || now.Before(completedAt) {
		return false, fmt.Errorf("invalid completion time")
	}

	fullKey, err := s.beginOperation(key)
	if err != nil {
		return false, err
	}
	if s.retention <= 0 || now.Sub(completedAt) < s.retention {
		return false, nil
	}

	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return false, mapS3Error("expire", key, err)
	}

	lastModified := aws.ToTime(head.LastModified).UTC()
	if lastModified.After(completedAt.UTC()) {
		return false, fmt.Errorf("artifact %q was modified after completion", key)
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return false, mapS3Error("delete expired", key, err)
	}

	return true, nil
}

func (s *S3) beginOperation(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}

	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return "", ErrClosed
	}

	if s.prefix == "" {
		return key, nil
	}

	return s.prefix + "/" + key, nil
}

func spoolBoundedArtifact(ctx context.Context, source io.Reader, maxBytes int64) (*os.File, int64, error) {
	tempFile, err := os.CreateTemp("", "kubereel-s3-*")
	if err != nil {
		return nil, 0, fmt.Errorf("create S3 staging file: %w", err)
	}

	limited := io.LimitReader(contextReader{ctx: ctx, source: source}, maxBytes+1)
	written, copyErr := io.Copy(tempFile, limited)
	if copyErr != nil {
		removeTempFile(tempFile)
		return nil, 0, copyErr
	}
	if written > maxBytes {
		removeTempFile(tempFile)
		return nil, 0, ErrTooLarge
	}

	return tempFile, written, nil
}

func sniffTempContentType(key string, tempFile *os.File) (string, error) {
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek staged S3 artifact: %w", err)
	}

	byExt := mime.TypeByExtension(strings.ToLower(path.Ext(key)))
	if byExt != "" {
		return byExt, nil
	}

	var header [512]byte
	n, readErr := tempFile.Read(header[:])
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("sniff staged S3 artifact: %w", readErr)
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind staged S3 artifact: %w", err)
	}

	if n == 0 {
		return "application/octet-stream", nil
	}

	return http.DetectContentType(header[:n]), nil
}

func removeTempFile(file *os.File) {
	if file == nil {
		return
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
}

func mapS3Error(action, key string, err error) error {
	if isS3NotFound(err) {
		return fmt.Errorf("%s S3 artifact %q: %w", action, key, fs.ErrNotExist)
	}
	return fmt.Errorf("%s S3 artifact %q: %w", action, key, err)
}

func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}

	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}

	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}

	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "NoSuchKey" || code == "NotFound" || code == "404"
	}

	return false
}

type s3Object struct {
	ctx     context.Context
	client  s3API
	bucket  string
	fullKey string
	name    string
	size    int64
	modTime time.Time

	offset int64
	stream io.ReadCloser
	limit  io.Reader
	closed bool
}

func (o *s3Object) Read(p []byte) (int, error) {
	if o.closed {
		return 0, fs.ErrClosed
	}
	if err := o.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if o.offset >= o.size {
		return 0, io.EOF
	}
	if err := o.ensureStream(); err != nil {
		return 0, err
	}

	n, err := o.limit.Read(p)
	o.offset += int64(n)
	if errors.Is(err, io.EOF) {
		o.closeStream()
	}

	return n, err
}

func (o *s3Object) Seek(offset int64, whence int) (int64, error) {
	if o.closed {
		return 0, fs.ErrClosed
	}

	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = o.offset + offset
	case io.SeekEnd:
		next = o.size + offset
	default:
		return 0, fmt.Errorf("unsupported seek whence %d: %w", whence, fs.ErrInvalid)
	}

	if next < 0 {
		return 0, fmt.Errorf("negative seek offset: %w", fs.ErrInvalid)
	}
	if next != o.offset {
		o.closeStream()
	}

	o.offset = next
	return next, nil
}

func (o *s3Object) Stat() (os.FileInfo, error) {
	if o.closed {
		return nil, fs.ErrClosed
	}

	return s3FileInfo{
		name:    o.name,
		size:    o.size,
		modTime: o.modTime,
	}, nil
}

func (o *s3Object) Close() error {
	if o.closed {
		return nil
	}
	o.closed = true
	return o.closeStream()
}

func (o *s3Object) ensureStream() error {
	if o.stream != nil {
		return nil
	}

	remaining := o.size - o.offset
	rangeHeader := fmt.Sprintf("bytes=%d-%d", o.offset, o.size-1)
	output, err := o.client.GetObject(o.ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(o.fullKey),
		Range:  aws.String(rangeHeader),
	})
	if err != nil {
		return mapS3Error("read", o.name, err)
	}

	o.stream = output.Body
	o.limit = io.LimitReader(output.Body, remaining)
	return nil
}

func (o *s3Object) closeStream() error {
	if o.stream == nil {
		return nil
	}
	err := o.stream.Close()
	o.stream = nil
	o.limit = nil
	return err
}

type s3FileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (i s3FileInfo) Name() string       { return i.name }
func (i s3FileInfo) Size() int64        { return i.size }
func (i s3FileInfo) Mode() fs.FileMode  { return 0600 }
func (i s3FileInfo) ModTime() time.Time { return i.modTime }
func (i s3FileInfo) IsDir() bool        { return false }
func (i s3FileInfo) Sys() any           { return nil }
