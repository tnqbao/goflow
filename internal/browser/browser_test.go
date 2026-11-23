package browser

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestStartMissingExecutable(t *testing.T) {
	_, err := Start(context.Background(), filepath.Join(t.TempDir(), "missing-chromium"))
	if err == nil {
		t.Fatal("start succeeded with a missing executable")
	}
}

func TestStartCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Start(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v, want context canceled", err)
	}
}

func TestBrowserLifecycle(t *testing.T) {
	path := browserExecutable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	browser, err := Start(ctx, path)
	if err != nil {
		t.Fatalf("start Chromium: %v", err)
	}
	defer browser.Close()
	var title string
	if err := chromedp.Run(browser.Context(), chromedp.Title(&title)); err != nil {
		t.Fatalf("browser operation: %v", err)
	}
	if err := browser.Close(); err != nil {
		t.Fatalf("close Chromium: %v", err)
	}
	if err := browser.Close(); err != nil {
		t.Fatalf("close Chromium again: %v", err)
	}
	select {
	case <-browser.Context().Done():
	default:
		t.Fatal("browser context remains active after close")
	}
}

func TestBrowserProcessExit(t *testing.T) {
	path := browserExecutable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	browser, err := Start(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	if err := chromedp.FromContext(browser.Context()).Browser.Process().Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-browser.Context().Done():
	case <-ctx.Done():
		t.Fatal("browser context did not close after the process exited")
	}
}

func browserExecutable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("google-chrome")
	if err == nil {
		return path
	}
	path, err = exec.LookPath("chromium")
	if err == nil {
		return path
	}
	t.Skip("Chromium not installed")
	return ""
}
