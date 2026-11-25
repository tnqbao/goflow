package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/config"
)

func TestBaseRoutes(t *testing.T) {
	db := openTestDatabase(t)

	tests := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/readyz", http.StatusOK},
		{http.MethodHead, "/healthz", http.StatusOK},
		{http.MethodHead, "/readyz", http.StatusOK},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodPost, "/readyz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/", http.StatusSeeOther},
		{http.MethodGet, "/settings/providers", http.StatusNotFound},
		{http.MethodGet, "/static/app.css", http.StatusOK},
		{http.MethodGet, "/static/missing.css", http.StatusNotFound},
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodGet, "/healthz/extra", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newBaseEcho(db).ServeHTTP(recorder, httptest.NewRequest(tt.method, tt.path, nil))
			if recorder.Code != tt.status {
				t.Errorf("status = %d, want %d", recorder.Code, tt.status)
			}
		})
	}
}

func TestRunServerListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	cfg := newTestConfig(t)
	cfg.Address = listener.Addr().String()

	if err := runServer(context.Background(), cfg); err == nil {
		t.Fatal("runServer succeeded on an occupied address")
	}
}

func TestServeHTTPStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	server := &http.Server{
		Addr: "127.0.0.1:0",
		BaseContext: func(net.Listener) context.Context {
			close(started)
			return context.Background()
		},
	}

	go func() {
		<-started
		cancel()
	}()

	if err := serveHTTP(ctx, server, time.Second); err != nil {
		t.Fatalf("serveHTTP after cancellation: %v", err)
	}
}

func TestServeHTTPClosesConnectionsAfterShutdownTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	address := make(chan string, 1)
	started := make(chan struct{})
	finished := make(chan struct{})

	server := &http.Server{
		Addr: "127.0.0.1:0",
		BaseContext: func(listener net.Listener) context.Context {
			address <- listener.Addr().String()
			return context.Background()
		},
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
			close(finished)
		}),
	}

	result := make(chan error, 1)
	go func() {
		result <- serveHTTP(ctx, server, 50*time.Millisecond)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := client.Get("http://" + <-address)
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-started
	cancel()

	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serveHTTP error = %v, want shutdown deadline exceeded", err)
	}

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("active handler was not cancelled")
	}

	<-requestDone
}

func TestWorkerAndRenderCommands(t *testing.T) {
	ensureTestFFmpegInPath(t)

	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	clipPath := filepath.Join(dir, "clip.mp4")
	renderSpecPath := filepath.Join(dir, "render.json")
	finalPath := filepath.Join(dir, "final.mp4")

	if err := os.WriteFile(requestPath, []byte(`{"type":"video","prompt":"A scene"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renderSpecPath, []byte(`{"operation":"normalize","inputs":["clip.mp4"]}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Database = filepath.Join(dir, "db")

	commands := [][]string{
		{"worker", "--input", requestPath, "--output", clipPath},
		{"render", "--input", renderSpecPath, "--output", finalPath},
	}
	for _, args := range commands {
		if err := executeCommand(context.Background(), args, cfg); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(finalPath); err != nil {
		t.Fatal(err)
	}

	if err := executeCommand(context.Background(), []string{"worker"}, config.Config{}); err == nil {
		t.Fatal("worker accepted missing paths")
	}
}

func TestRenderCommandFFmpegSpec(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Database = filepath.Join(dir, "kubereel.db")

	badSpecPath := filepath.Join(dir, "bad.json")
	outPath := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(badSpecPath, []byte(`{"operation":"shell","inputs":["a.mp4"]}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := executeCommand(context.Background(), []string{"render", "--input", badSpecPath, "--output", outPath}, cfg); err == nil {
		t.Fatal("render accepted an unsupported operation")
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return
	}

	sourceClip := filepath.Join(dir, "clip.mp4")
	gen := exec.Command("ffmpeg", "-nostdin", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=320x240:r=10:d=1", "-an", "-c:v", "mpeg4", sourceClip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("create test clip: %v\n%s", err, out)
	}

	specPath := filepath.Join(dir, "render.json")
	specJSON := `{"operation":"resize","inputs":["clip.mp4"],"width":640,"height":360}`
	if err := os.WriteFile(specPath, []byte(specJSON), 0600); err != nil {
		t.Fatal(err)
	}

	if err := executeCommand(context.Background(), []string{"render", "--input", specPath, "--output", outPath}, cfg); err != nil {
		t.Fatalf("executeCommand render: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("rendered MP4 stat = %v, %v", info, err)
	}
}

func TestSingleExecutableCommands(t *testing.T) {
	ensureTestFFmpegInPath(t)

	dir := t.TempDir()
	binary := filepath.Join(dir, "kubereel")

	buildCmd := exec.Command("go", "build", "-o", binary, ".")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v\n%s", err, output)
	}

	llmRequestPath := filepath.Join(dir, "llm.json")
	scriptPath := filepath.Join(dir, "script.json")
	videoRequestPath := filepath.Join(dir, "video.json")
	clipPath := filepath.Join(dir, "clip.mp4")
	renderSpecPath := filepath.Join(dir, "render.json")
	resultPath := filepath.Join(dir, "result.mp4")

	if err := os.WriteFile(llmRequestPath, []byte(`{"type":"llm","prompt":"Single executable"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoRequestPath, []byte(`{"type":"video","prompt":"Single executable"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renderSpecPath, []byte(`{"operation":"resize","inputs":["clip.mp4"],"width":640,"height":360}`), 0600); err != nil {
		t.Fatal(err)
	}

	commands := [][]string{
		{"worker", "--input", llmRequestPath, "--output", scriptPath},
		{"worker", "--input", videoRequestPath, "--output", clipPath},
		{"render", "--input", renderSpecPath, "--output", resultPath},
	}
	for _, args := range commands {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), config.EnvDataDir+"="+dir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s command: %v\n%s", args[0], err, output)
		}
	}

	scriptData, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scriptData), "Single executable") {
		t.Fatal("script artifact does not contain the request")
	}

	mp4Data, err := os.ReadFile(resultPath)
	if err != nil || len(mp4Data) < 24 || string(mp4Data[4:8]) != "ftyp" {
		t.Fatalf("rendered MP4 is invalid (len=%d, err=%v)", len(mp4Data), err)
	}

	if err := exec.Command(binary, "unknown").Run(); err == nil {
		t.Fatal("executable accepted an unknown command")
	}
}

func ensureTestFFmpegInPath(t *testing.T) {
	t.Helper()

	_, hasFFmpeg := exec.LookPath("ffmpeg")
	_, hasFFprobe := exec.LookPath("ffprobe")
	if hasFFmpeg == nil && hasFFprobe == nil {
		return
	}

	binDir := t.TempDir()
	templateMP4 := filepath.Join(binDir, "sample.mp4")

	var buf bytes.Buffer
	writeBox := func(boxType string, payload []byte) {
		var header [8]byte
		binary.BigEndian.PutUint32(header[:4], uint32(8+len(payload)))
		copy(header[4:8], boxType)
		buf.Write(header[:])
		buf.Write(payload)
	}
	writeBox("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	writeBox("moov", []byte("movie-header"))
	writeBox("mdat", []byte("video-frames"))

	if err := os.WriteFile(templateMP4, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	ffmpegBody := "#!/bin/sh\nset -eu\nout=\"\"\nfor arg in \"$@\"; do out=\"$arg\"; done\n" +
		"if [ \"$out\" = \"pipe:1\" ]; then cat \"" + templateMP4 + "\"; else cp \"" + templateMP4 + "\" \"$out\"; fi\n"
	if err := os.WriteFile(filepath.Join(binDir, "ffmpeg"), []byte(ffmpegBody), 0700); err != nil {
		t.Fatal(err)
	}

	ffprobeBody := `#!/bin/sh
set -eu
target=""
for arg in "$@"; do target="$arg"; done
case "$target" in
  *output.mp4)
    printf '{"format":{"format_name":"mov,mp4"},"streams":[{"codec_type":"video","codec_name":"h264","width":640,"height":360},{"codec_type":"video","codec_name":"h264","width":320,"height":240}]}\n'
    ;;
  *)
    printf '{"format":{"format_name":"mov,mp4"},"streams":[{"codec_type":"video","codec_name":"mpeg4","width":320,"height":240}]}\n'
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "ffprobe"), []byte(ffprobeBody), 0700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestEmbeddedAssets(t *testing.T) {
	handler := newBaseEcho(openTestDatabase(t))

	home := httptest.NewRecorder()
	handler.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := home.Header().Get("Location"); got != "/workflows" {
		t.Errorf("home redirect = %q, want /workflows", got)
	}

	stylesheet := httptest.NewRecorder()
	handler.ServeHTTP(stylesheet, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if !strings.Contains(stylesheet.Header().Get("Content-Type"), "text/css") {
		t.Errorf("content type = %q, want text/css", stylesheet.Header().Get("Content-Type"))
	}
	if !strings.Contains(stylesheet.Body.String(), "font-family") {
		t.Error("stylesheet missing font-family")
	}
}

func TestOpenDatabasePreservesData(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t)

	db, err := openDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE saved (id INTEGER); INSERT INTO saved VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var id int
	if err := db.QueryRowContext(ctx, `SELECT id FROM saved`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Errorf("saved id = %d, want 42", id)
	}
}

func TestOpenDatabaseFailure(t *testing.T) {
	cfg := newTestConfig(t)
	if err := os.WriteFile(filepath.Dir(cfg.Database), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}

	if db, err := openDatabase(context.Background(), cfg); err == nil {
		db.Close()
		t.Fatal("initialization succeeded with a file as data directory")
	}
}

func TestReadinessDatabaseFailure(t *testing.T) {
	db := openTestDatabase(t)
	handler := newBaseEcho(db)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path   string
		status int
	}{
		{"/readyz", http.StatusServiceUnavailable},
		{"/healthz", http.StatusOK},
	}

	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if recorder.Code != tt.status {
			t.Errorf("%s status = %d, want %d", tt.path, recorder.Code, tt.status)
		}
	}
}

func newTestConfig(t *testing.T) config.Config {
	t.Helper()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Address = "127.0.0.1:0"
	cfg.Database = filepath.Join(dir, "data", "kubereel.db")

	return cfg
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	db, err := openDatabase(context.Background(), newTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}
