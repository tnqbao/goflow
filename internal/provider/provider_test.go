package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type testVideoProvider struct{}

func (testVideoProvider) Submit(ctx context.Context, request VideoRequest) (GenerationHandle, error) {
	if err := ctx.Err(); err != nil {
		return GenerationHandle{}, err
	}
	if request.Prompt == "uncertain" {
		return GenerationHandle{}, fmt.Errorf("submit: %w", ErrSubmissionUncertain)
	}
	return GenerationHandle{ID: request.Prompt}, nil
}
func (testVideoProvider) GetStatus(ctx context.Context, _ GenerationHandle) (GenerationStatus, error) {
	if err := ctx.Err(); err != nil {
		return GenerationStatus{}, err
	}
	return GenerationStatus{State: GenerationSucceeded}, nil
}
func (testVideoProvider) GetResult(ctx context.Context, handle GenerationHandle) (MediaResult, error) {
	if err := ctx.Err(); err != nil {
		return MediaResult{}, err
	}
	return MediaResult{Body: io.NopCloser(strings.NewReader(handle.ID)), ContentType: "video/mp4"}, nil
}

var _ VideoProvider = testVideoProvider{}

func TestVideoProviderLifecycle(t *testing.T) {
	var adapter VideoProvider = testVideoProvider{}
	ctx := context.Background()
	handle, err := adapter.Submit(ctx, VideoRequest{Prompt: "scene"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(handle)
	if err != nil {
		t.Fatal(err)
	}
	var recovered GenerationHandle
	if err := json.Unmarshal(encoded, &recovered); err != nil {
		t.Fatal(err)
	}
	status, err := adapter.GetStatus(ctx, recovered)
	if err != nil || status.State != GenerationSucceeded {
		t.Fatalf("status = %+v, %v", status, err)
	}
	result, err := adapter.GetResult(ctx, recovered)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(result.Body)
	if err := errors.Join(readErr, result.Body.Close()); err != nil {
		t.Fatal(err)
	}
	if string(data) != "scene" || result.ContentType != "video/mp4" {
		t.Fatalf("result = %q, %q", data, result.ContentType)
	}
	if _, err := adapter.Submit(ctx, VideoRequest{Prompt: "uncertain"}); !errors.Is(err, ErrSubmissionUncertain) {
		t.Fatalf("uncertain submission: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := adapter.Submit(cancelled, VideoRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled submission: %v", err)
	}
}
