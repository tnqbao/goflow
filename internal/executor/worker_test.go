package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/storage"
)

type fakeTextProvider struct {
	text string
	err  error
}

func (f fakeTextProvider) Generate(_ context.Context, _ provider.TextRequest) (provider.TextResult, error) {
	if f.err != nil {
		return provider.TextResult{}, f.err
	}

	return provider.TextResult{Text: f.text}, nil
}

func TestExecuteGeminiSuccessAndUncertainMarker(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewFilesystem(t.TempDir(), 4<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const workflowID = "0123456789abcdef0123456789abcdef"
	outputKey := "workflows/" + workflowID + "/script.json"

	req := StepRequest{
		Provider: ProviderGemini,
		Type:     mock.StepLLM,
		Model:    "gemini-4-hydrogen",
		Prompt:   "Forest mystery",
	}

	err = executeGeminiTextStep(
		ctx,
		store,
		req,
		outputKey,
		fakeTextProvider{text: "Scene 1: Pine trees\nScene 2: Flickering lantern"},
	)
	if err != nil {
		t.Fatalf("executeGeminiTextStep: %v", err)
	}
	if err := mock.ValidateScript(ctx, store, outputKey); err != nil {
		t.Fatalf("ValidateScript: %v", err)
	}

	uncertain, err := HasUncertainOutcome(ctx, store, workflowID)
	if err != nil || uncertain {
		t.Fatalf("unexpected uncertain marker: %v, %v", uncertain, err)
	}

	err = executeGeminiTextStep(
		ctx,
		store,
		req,
		outputKey,
		fakeTextProvider{err: provider.ErrSubmissionUncertain},
	)
	if !errors.Is(err, provider.ErrSubmissionUncertain) {
		t.Fatalf("err = %v, want ErrSubmissionUncertain", err)
	}

	uncertain, err = HasUncertainOutcome(ctx, store, workflowID)
	if err != nil || !uncertain {
		t.Fatalf("expected uncertain marker after ErrSubmissionUncertain: %v, %v", uncertain, err)
	}

	if err := store.Put(ctx, "mock-req.json", bytes.NewReader([]byte(`{"type":"llm","prompt":"Local mock"}`))); err != nil {
		t.Fatal(err)
	}
	if err := Execute(ctx, store, "mock-req.json", "mock-script.json"); err != nil {
		t.Fatalf("Execute mock: %v", err)
	}
}

type fakeAsyncMediaProvider struct {
	submitErr   error
	statuses    []provider.GenerationStatus
	mediaBytes  []byte
	contentType string
	gotImage    []byte
	polls       atomic.Int32
	cancelled   atomic.Bool
}

func (f *fakeAsyncMediaProvider) Submit(_ context.Context, req provider.ImageRequest) (provider.GenerationHandle, error) {
	if f.submitErr != nil {
		return provider.GenerationHandle{}, f.submitErr
	}
	if req.Prompt == "" || req.Model == "" {
		return provider.GenerationHandle{}, errors.New("missing model or prompt")
	}

	return provider.GenerationHandle{ID: "fal-ai/flux:req-img-1"}, nil
}

func (f *fakeAsyncMediaProvider) SubmitVideo(_ context.Context, req provider.VideoRequest) (provider.GenerationHandle, error) {
	if f.submitErr != nil {
		return provider.GenerationHandle{}, f.submitErr
	}
	if req.Image != nil {
		data, err := io.ReadAll(req.Image)
		if err != nil {
			return provider.GenerationHandle{}, err
		}
		f.gotImage = data
	}

	return provider.GenerationHandle{ID: "fal-ai/kling-video:req-vid-1"}, nil
}

func (f *fakeAsyncMediaProvider) GetStatus(_ context.Context, _ provider.GenerationHandle) (provider.GenerationStatus, error) {
	idx := int(f.polls.Add(1)) - 1
	if idx >= len(f.statuses) {
		idx = len(f.statuses) - 1
	}

	return f.statuses[idx], nil
}

func (f *fakeAsyncMediaProvider) GetResult(_ context.Context, _ provider.GenerationHandle) (provider.MediaResult, error) {
	return provider.MediaResult{
		Body:        io.NopCloser(bytes.NewReader(f.mediaBytes)),
		ContentType: f.contentType,
	}, nil
}

func (f *fakeAsyncMediaProvider) Cancel(_ context.Context, _ provider.GenerationHandle) error {
	f.cancelled.Store(true)
	return nil
}

type fakeVideoAdapter struct {
	*fakeAsyncMediaProvider
}

func (v fakeVideoAdapter) Submit(ctx context.Context, req provider.VideoRequest) (provider.GenerationHandle, error) {
	return v.SubmitVideo(ctx, req)
}

func TestExecuteFalImageVideoCancelAndUncertain(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewFilesystem(t.TempDir(), 4<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d}
	imgFake := &fakeAsyncMediaProvider{
		statuses: []provider.GenerationStatus{
			{State: provider.GenerationPending},
			{State: provider.GenerationRunning},
			{State: provider.GenerationSucceeded},
		},
		mediaBytes:  pngBytes,
		contentType: "image/png",
	}

	const imgWorkflowID = "11111111111111111111111111111111"
	imgKey := "workflows/" + imgWorkflowID + "/image.png"
	err = runFalImageStep(ctx, store, imgFake, StepRequest{
		Provider: ProviderFal,
		Type:     mock.StepImage,
		Model:    "fal-ai/flux/dev",
		Prompt:   "Misty mountain cabin",
	}, imgKey, time.Millisecond)
	if err != nil {
		t.Fatalf("runFalImageStep: %v", err)
	}
	if err := ValidateImageArtifact(ctx, store, imgKey); err != nil {
		t.Fatalf("ValidateImageArtifact PNG: %v", err)
	}

	mp4Bytes := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	vidFake := &fakeAsyncMediaProvider{
		statuses: []provider.GenerationStatus{
			{State: provider.GenerationPending},
			{State: provider.GenerationSucceeded},
		},
		mediaBytes:  mp4Bytes,
		contentType: "video/mp4",
	}

	const vidWorkflowID = "22222222222222222222222222222222"
	vidKey := "workflows/" + vidWorkflowID + "/clip.mp4"
	err = runFalVideoStep(ctx, store, fakeVideoAdapter{vidFake}, StepRequest{
		Provider:   ProviderFal,
		Type:       mock.StepVideo,
		Model:      "fal-ai/kling-video/v1.6/standard/image-to-video",
		ImageInput: imgKey,
		Prompt:     "Slow camera push toward cabin",
	}, vidKey, time.Millisecond)
	if err != nil {
		t.Fatalf("runFalVideoStep: %v", err)
	}
	if !bytes.Equal(vidFake.gotImage, pngBytes) {
		t.Fatalf("reference image = %x, want %x", vidFake.gotImage, pngBytes)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancelFake := &fakeAsyncMediaProvider{
		statuses: []provider.GenerationStatus{{State: provider.GenerationPending}},
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	err = runFalImageStep(cancelCtx, store, cancelFake, StepRequest{
		Provider: ProviderFal,
		Type:     mock.StepImage,
		Model:    "fal-ai/flux/dev",
		Prompt:   "Cancelled job",
	}, "workflows/33333333333333333333333333333333/image.png", 50*time.Millisecond)
	if !errors.Is(err, context.Canceled) || !cancelFake.cancelled.Load() {
		t.Fatalf("expected context.Canceled and provider Cancel(), got err=%v cancelled=%v", err, cancelFake.cancelled.Load())
	}

	const uncertainID = "44444444444444444444444444444444"
	uncertainFake := &fakeAsyncMediaProvider{submitErr: provider.ErrSubmissionUncertain}
	err = runFalVideoStep(ctx, store, fakeVideoAdapter{uncertainFake}, StepRequest{
		Provider: ProviderFal,
		Type:     mock.StepVideo,
		Model:    "fal-ai/wan-t2v",
		Prompt:   "Ocean storm",
	}, "workflows/"+uncertainID+"/clip.mp4", time.Millisecond)
	if !errors.Is(err, provider.ErrSubmissionUncertain) {
		t.Fatalf("err = %v, want ErrSubmissionUncertain", err)
	}
	uncertain, err := HasUncertainOutcome(ctx, store, uncertainID)
	if err != nil || !uncertain {
		t.Fatalf("expected uncertain marker: %v, %v", uncertain, err)
	}
}

func TestValidateImageArtifactFormats(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewFilesystem(t.TempDir(), 4<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	validCases := map[string][]byte{
		"a.png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3, 4},
		"b.jpg":  {0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10},
		"c.webp": {'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P'},
	}
	for name, payload := range validCases {
		if err := store.Put(ctx, name, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		if err := ValidateImageArtifact(ctx, store, name); err != nil {
			t.Fatalf("ValidateImageArtifact(%s): %v", name, err)
		}
	}

	if err := store.Put(ctx, "bad.png", bytes.NewReader([]byte("<html>not an image</html>"))); err != nil {
		t.Fatal(err)
	}
	if err := ValidateImageArtifact(ctx, store, "bad.png"); err == nil {
		t.Fatal("expected error for invalid raster bytes")
	}
}
