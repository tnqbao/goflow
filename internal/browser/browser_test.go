package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/tnqbao/goflow/internal/config"
)

func TestStartMissingExecutable(t *testing.T) {
	_, err := Start(context.Background(), filepath.Join(t.TempDir(), "missing-chromium"), filepath.Join(t.TempDir(), "profile"))
	if err == nil {
		t.Fatal("start succeeded with a missing executable")
	}
}

func TestStartCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Start(ctx, "", filepath.Join(t.TempDir(), "profile"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v, want context canceled", err)
	}
}

func TestBrowserLifecycle(t *testing.T) {
	path := browserExecutable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	browser, err := Start(ctx, path, filepath.Join(t.TempDir(), "profile"))
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
	browser, err := Start(ctx, path, filepath.Join(t.TempDir(), "profile"))
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	if err := chromedp.FromContext(browser.Context()).Browser.Process().Signal(syscall.SIGTERM); err != nil {
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

func TestInvalidProfile(t *testing.T) {
	ctx := context.Background()
	if _, err := Start(ctx, "", ""); err == nil {
		t.Error("empty profile directory was accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, "", filepath.Join(file, "profile")); err == nil {
		t.Error("profile directory inside a file was accepted")
	}
}

func TestProfilePersistsAcrossRestarts(t *testing.T) {
	path := browserExecutable(t)
	t.Setenv("GOFLOW_DATA_DIR", t.TempDir())
	profile := config.Load().BrowserData
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first, err := Start(ctx, path, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := chromedp.Run(first.Context(), chromedp.Navigate(server.URL), chromedp.Evaluate(`localStorage.setItem("goflow-test", "saved")`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Start(ctx, path, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var value string
	if err := chromedp.Run(second.Context(), chromedp.Navigate(server.URL), chromedp.Evaluate(`localStorage.getItem("goflow-test")`, &value)); err != nil {
		t.Fatal(err)
	}
	if value != "saved" {
		t.Errorf("saved browser data = %q, want saved", value)
	}
	info, err := os.Stat(profile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("profile directory permissions = %o, want no group or other access", info.Mode().Perm())
	}
}

func TestProfileInUse(t *testing.T) {
	path := browserExecutable(t)
	profile := filepath.Join(t.TempDir(), "profile")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, err := Start(ctx, path, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Start(ctx, path, profile)
	if err == nil {
		second.Close()
		t.Fatal("second Chromium process started with a profile in use")
	}
	if err := chromedp.Run(first.Context(), chromedp.Title(new(string))); err != nil {
		t.Fatalf("original browser stopped after second launch: %v", err)
	}
}
