package executor

import (
	"bytes"
	"context"
	"errors"
	"testing"

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
