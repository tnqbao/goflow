// Package browser manages a local Chromium process.
package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// Browser owns one Chromium process and its persistent profile.
type Browser struct {
	ctx       context.Context
	cancel    context.CancelFunc
	allocator context.CancelFunc
	once      sync.Once
	closeErr  error
}

// Start launches Chromium with the supplied profile directory and waits for a
// usable browser session. An empty executable lets chromedp find Chromium.
// Canceling ctx stops the browser; the profile is retained after shutdown.
func Start(ctx context.Context, executable, profileDir string) (*Browser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start Chromium: %w", err)
	}
	if profileDir == "" {
		return nil, fmt.Errorf("browser profile directory is empty")
	}
	profileDir, err := filepath.Abs(profileDir)
	if err != nil {
		return nil, fmt.Errorf("resolve browser profile directory: %w", err)
	}
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return nil, fmt.Errorf("create browser profile directory: %w", err)
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("no-sandbox", false),
		chromedp.Flag("remote-debugging-address", "127.0.0.1"),
	)
	if executable != "" {
		opts = append(opts, chromedp.ExecPath(executable))
	}
	allocatorCtx, allocatorCancel := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx)
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocatorCancel()
		return nil, fmt.Errorf("start Chromium: %w", err)
	}
	return &Browser{ctx: browserCtx, cancel: browserCancel, allocator: allocatorCancel}, nil
}

// Context returns the browser context for browser operations. It is canceled
// when the browser stops or its parent context is canceled.
func (b *Browser) Context() context.Context {
	return b.ctx
}

// Close stops Chromium and waits for its process to exit.
// It is safe to call more than once.
func (b *Browser) Close() error {
	b.once.Do(func() {
		defer b.allocator()
		defer b.cancel()
		ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
		defer cancel()
		if err := chromedp.Cancel(ctx); err != nil {
			b.closeErr = fmt.Errorf("stop Chromium: %w", err)
		}
	})
	return b.closeErr
}
