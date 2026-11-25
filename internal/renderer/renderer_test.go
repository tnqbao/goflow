package renderer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/storage"
)

func sampleMP4Bytes() []byte {
	var buf bytes.Buffer

	writeBox := func(boxType string, payload []byte) {
		var header [8]byte
		binary.BigEndian.PutUint32(header[:4], uint32(8+len(payload)))
		copy(header[4:8], boxType)
		buf.Write(header[:])
		buf.Write(payload)
	}

	writeBox("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	writeBox("moov", []byte("minimal-movie-header"))
	writeBox("mdat", []byte("encoded-video-frames"))

	return buf.Bytes()
}

func newTestStore(t *testing.T) *storage.Filesystem {
	t.Helper()

	store, err := storage.NewFilesystem(t.TempDir(), 64<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})

	return store
}

func TestValidateRequestAndDecode(t *testing.T) {
	validCases := []Request{
		{Operation: OpMerge, Inputs: []string{"clips/a.mp4", "clips/b.mov"}},
		{Operation: OpTrim, Inputs: []string{"clips/a.mp4"}, StartSeconds: 0.5, DurationSeconds: 1.5},
		{Operation: OpResize, Inputs: []string{"clips/a.mp4"}, Width: 640, Height: 360},
		{Operation: OpAspectRatio, Inputs: []string{"clips/a.mp4"}, AspectRatio: "9:16"},
		{Operation: OpAddAudio, Inputs: []string{"clips/a.mp4"}, AudioInput: "audio/track.wav"},
		{Operation: OpNormalize, Inputs: []string{"clips/a.webm"}, FPS: 30},
		{
			Operation:       OpRenderMP4,
			Inputs:          []string{"clips/a.mp4", "clips/b.mp4"},
			AudioInput:      "audio/music.aac",
			AspectRatio:     "16:9",
			DurationSeconds: 2.0,
			FPS:             24,
		},
	}

	for _, req := range validCases {
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeRequest(raw); err != nil {
			t.Fatalf("DecodeRequest(%+v) = %v", req, err)
		}
	}

	invalidCases := []Request{
		{Operation: "shell", Inputs: []string{"a.mp4"}},
		{Operation: OpMerge, Inputs: []string{"only-one.mp4"}},
		{Operation: OpTrim, Inputs: []string{"a.mp4", "b.mp4"}, DurationSeconds: 1},
		{Operation: OpTrim, Inputs: []string{"a.mp4"}, DurationSeconds: 0},
		{Operation: OpTrim, Inputs: []string{"a.mp4"}, StartSeconds: -1, DurationSeconds: 1},
		{Operation: OpTrim, Inputs: []string{"a.mp4"}, DurationSeconds: math.Inf(1)},
		{Operation: OpResize, Inputs: []string{"a.mp4"}, Width: 641, Height: 360},
		{Operation: OpResize, Inputs: []string{"a.mp4"}, Width: 8, Height: 360},
		{Operation: OpAspectRatio, Inputs: []string{"a.mp4"}, AspectRatio: "21:9"},
		{Operation: OpRenderMP4, Inputs: []string{"a.mp4"}, Width: 640, Height: 360, AspectRatio: "16:9"},
		{Operation: OpAddAudio, Inputs: []string{"a.mp4"}},
		{Operation: OpNormalize, Inputs: []string{"a.mp4"}, AudioInput: "track.mp3"},
		{Operation: OpNormalize, Inputs: []string{"../secret.mp4"}},
		{Operation: OpNormalize, Inputs: []string{"-option.mp4"}},
		{Operation: OpNormalize, Inputs: []string{".hidden/clip.mp4"}},
		{Operation: OpNormalize, Inputs: []string{"kubereel.db"}},
		{Operation: OpNormalize, Inputs: []string{"scene.svg"}},
		{Operation: OpNormalize, Inputs: []string{"a.mp4"}, FPS: 120},
	}

	for _, req := range invalidCases {
		if err := ValidateRequest(req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("ValidateRequest(%+v) = %v, want ErrInvalidRequest", req, err)
		}
	}

	unknownField := []byte(`{"operation":"normalize","inputs":["a.mp4"],"rawArgs":["-f","lavfi"]}`)
	if _, err := DecodeRequest(unknownField); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown field error = %v", err)
	}

	trailing := []byte(`{"operation":"normalize","inputs":["a.mp4"]}{"extra":true}`)
	if _, err := DecodeRequest(trailing); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("trailing JSON error = %v", err)
	}

	for _, badOut := range []string{"out.mkv", "../out.mp4", "-out.mp4", ".out.mp4"} {
		if err := ValidateOutputKey(badOut); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("ValidateOutputKey(%q) = %v", badOut, err)
		}
	}
}

func TestValidateMP4Container(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.Put(ctx, "valid.mp4", bytes.NewReader(sampleMP4Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMP4(ctx, store, "valid.mp4"); err != nil {
		t.Fatalf("ValidateMP4 valid = %v", err)
	}

	var largeBox bytes.Buffer
	largeBox.Write([]byte{0, 0, 0, 20, 'f', 't', 'y', 'p'})
	largeBox.Write([]byte("isom\x00\x00\x02\x00mp41"))
	largeBox.Write([]byte{0, 0, 0, 12, 'm', 'o', 'o', 'v', 1, 2, 3, 4})
	largeBox.Write([]byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0, 0, 0, 0, 0, 0, 0, 20, 9, 9, 9, 9})
	if err := store.Put(ctx, "large.mp4", bytes.NewReader(largeBox.Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMP4(ctx, store, "large.mp4"); err != nil {
		t.Fatalf("ValidateMP4 64-bit box = %v", err)
	}

	invalidPayloads := map[string][]byte{
		"short.mp4":      []byte("not-an-mp4"),
		"no-moov.mp4":    append([]byte{0, 0, 0, 20, 'f', 't', 'y', 'p'}, []byte("isom0000mp41\x00\x00\x00\x0cmdat1234")...),
		"bad-size.mp4":   append([]byte{0, 0, 0, 20, 'f', 't', 'y', 'p'}, []byte("isom0000mp41\x00\x00\x00\xffmoov1234")...),
		"not-ftyp.mp4":   []byte("\x00\x00\x00\x10free12345678\x00\x00\x00\x0cmoov1234\x00\x00\x00\x0cmdat1234"),
		"empty-mdat.mp4": append([]byte{0, 0, 0, 20, 'f', 't', 'y', 'p'}, []byte("isom0000mp41\x00\x00\x00\x0cmoov1234\x00\x00\x00\x08mdat")...),
	}
	for name, payload := range invalidPayloads {
		if err := store.Put(ctx, name, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		if err := ValidateMP4(ctx, store, name); err == nil {
			t.Fatalf("ValidateMP4(%s) unexpectedly succeeded", name)
		}
	}
}

func TestRendererFakeBinariesAndSecurity(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	binDir := t.TempDir()
	argsLog := filepath.Join(binDir, "ffmpeg-args.txt")

	ffprobeScript := filepath.Join(binDir, "ffprobe")
	ffprobeBody := `#!/bin/sh
set -eu
target=""
for arg in "$@"; do target="$arg"; done
case "$target" in
  *output.mp4)
    width="${FAKE_OUT_WIDTH:-640}"
    height="${FAKE_OUT_HEIGHT:-360}"
    printf '{"format":{"format_name":"mov,mp4,m4a"},"streams":[{"codec_type":"video","codec_name":"h264","width":%s,"height":%s},{"codec_type":"audio","codec_name":"aac"}]}\n' "$width" "$height"
    ;;
  *audio.media)
    printf '{"format":{"format_name":"wav"},"streams":[{"codec_type":"audio","codec_name":"pcm_s16le"}]}\n'
    ;;
  *)
    fmt="${FAKE_INPUT_FORMAT:-mov,mp4}"
    printf '{"format":{"format_name":"%s"},"streams":[{"codec_type":"video","codec_name":"mpeg4","width":320,"height":240}]}\n' "$fmt"
    ;;
esac
`
	if err := os.WriteFile(ffprobeScript, []byte(ffprobeBody), 0700); err != nil {
		t.Fatal(err)
	}

	ffmpegScript := filepath.Join(binDir, "ffmpeg")
	ffmpegBody := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" > " + argsLog + "\n" +
		"if [ \"${FAKE_SLEEP:-0}\" = \"1\" ]; then exec sleep 10; fi\n" +
		"out=\"\"\nfor arg in \"$@\"; do out=\"$arg\"; done\n" +
		"cp \"" + filepath.Join(binDir, "template.mp4") + "\" \"$out\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "template.mp4"), sampleMP4Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ffmpegScript, []byte(ffmpegBody), 0700); err != nil {
		t.Fatal(err)
	}

	if err := store.Put(ctx, "clips/one.mp4", bytes.NewReader(sampleMP4Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "clips/two.mp4", bytes.NewReader(sampleMP4Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "audio/bg.wav", bytes.NewReader([]byte("RIFF-fake-wav"))); err != nil {
		t.Fatal(err)
	}

	r := &Renderer{FFmpegPath: ffmpegScript, FFprobePath: ffprobeScript}

	t.Setenv("FAKE_OUT_WIDTH", "720")
	t.Setenv("FAKE_OUT_HEIGHT", "1280")
	req := Request{
		Operation:   OpRenderMP4,
		Inputs:      []string{"clips/one.mp4", "clips/two.mp4"},
		AudioInput:  "audio/bg.wav",
		AspectRatio: "9:16",
		FPS:         24,
	}
	if err := r.Render(ctx, store, req, "renders/final.mp4"); err != nil {
		t.Fatalf("Render = %v", err)
	}
	if err := ValidateMP4(ctx, store, "renders/final.mp4"); err != nil {
		t.Fatalf("ValidateMP4 output = %v", err)
	}

	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	argText := string(logged)
	for _, mustHave := range []string{"-nostdin", "-protocol_whitelist\nfile", "concat=n=2:v=1:a=0[outv]", "libx264", "+faststart", "input-0.media"} {
		if !strings.Contains(argText, mustHave) {
			t.Fatalf("ffmpeg args missing %q:\n%s", mustHave, argText)
		}
	}
	if strings.Contains(argText, "clips/one.mp4") {
		t.Fatal("ffmpeg received user storage path instead of isolated scratch path")
	}

	t.Setenv("FAKE_INPUT_FORMAT", "hls,applehttp")
	if err := r.Render(ctx, store, Request{Operation: OpNormalize, Inputs: []string{"clips/one.mp4"}}, "renders/hls.mp4"); err == nil {
		t.Fatal("accepted disallowed HLS playlist demuxer")
	}
	t.Setenv("FAKE_INPUT_FORMAT", "mov,mp4")

	t.Setenv("FAKE_SLEEP", "1")
	cancelCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := r.Render(cancelCtx, store, Request{Operation: OpNormalize, Inputs: []string{"clips/one.mp4"}}, "renders/cancelled.mp4"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled Render = %v, want DeadlineExceeded", err)
	}
}

func TestRendererRealFFmpegOperations(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed on this host")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed on this host")
	}

	ctx := context.Background()
	store := newTestStore(t)
	temp := t.TempDir()

	clipA := filepath.Join(temp, "clipA.mp4")
	runFFmpegTestHelper(t, "-f", "lavfi", "-i", "color=c=navy:s=320x240:r=10:d=1.2", "-an", "-c:v", "mpeg4", clipA)
	putFileIntoStore(t, store, "inputs/clipA.mp4", clipA)

	clipB := filepath.Join(temp, "clipB.mp4")
	runFFmpegTestHelper(t, "-f", "lavfi", "-i", "color=c=darkred:s=480x360:r=15:d=1.0", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", clipB)
	putFileIntoStore(t, store, "inputs/clipB.mp4", clipB)

	audioClip := filepath.Join(temp, "tone.wav")
	runFFmpegTestHelper(t, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "1.5", audioClip)
	putFileIntoStore(t, store, "inputs/tone.wav", audioClip)

	r := New()
	cases := []struct {
		name      string
		req       Request
		wantW     int
		wantH     int
		wantAudio bool
	}{
		{
			name:  "merge",
			req:   Request{Operation: OpMerge, Inputs: []string{"inputs/clipA.mp4", "inputs/clipB.mp4"}},
			wantW: 320, wantH: 240,
		},
		{
			name:  "trim",
			req:   Request{Operation: OpTrim, Inputs: []string{"inputs/clipA.mp4"}, StartSeconds: 0.2, DurationSeconds: 0.6},
			wantW: 320, wantH: 240,
		},
		{
			name:  "resize",
			req:   Request{Operation: OpResize, Inputs: []string{"inputs/clipA.mp4"}, Width: 640, Height: 360},
			wantW: 640, wantH: 360,
		},
		{
			name:  "aspect_ratio",
			req:   Request{Operation: OpAspectRatio, Inputs: []string{"inputs/clipB.mp4"}, AspectRatio: "9:16"},
			wantW: 720, wantH: 1280,
		},
		{
			name:      "add_audio",
			req:       Request{Operation: OpAddAudio, Inputs: []string{"inputs/clipA.mp4"}, AudioInput: "inputs/tone.wav"},
			wantW:     320,
			wantH:     240,
			wantAudio: true,
		},
		{
			name:  "normalize",
			req:   Request{Operation: OpNormalize, Inputs: []string{"inputs/clipA.mp4"}},
			wantW: 320, wantH: 240,
		},
		{
			name: "render_mp4",
			req: Request{
				Operation:       OpRenderMP4,
				Inputs:          []string{"inputs/clipA.mp4", "inputs/clipB.mp4"},
				AudioInput:      "inputs/tone.wav",
				AspectRatio:     "1:1",
				DurationSeconds: 1.5,
				FPS:             24,
			},
			wantW:     720,
			wantH:     720,
			wantAudio: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqKey := "requests/" + tc.name + ".json"
			outKey := "outputs/" + tc.name + ".mp4"
			payload, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Put(ctx, reqKey, bytes.NewReader(payload)); err != nil {
				t.Fatal(err)
			}
			if err := r.RenderFile(ctx, store, reqKey, outKey); err != nil {
				t.Fatalf("RenderFile(%s) = %v", tc.name, err)
			}
			if err := ValidateMP4(ctx, store, outKey); err != nil {
				t.Fatalf("ValidateMP4(%s) = %v", tc.name, err)
			}
		})
	}
}

func runFFmpegTestHelper(t *testing.T, args ...string) {
	t.Helper()
	fullArgs := append([]string{"-nostdin", "-y", "-v", "error"}, args...)
	if out, err := exec.Command("ffmpeg", fullArgs...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg setup %v: %v\n%s", args, err, out)
	}
}

func putFileIntoStore(t *testing.T, store storage.Store, key, localPath string) {
	t.Helper()
	file, err := os.Open(localPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := store.Put(context.Background(), key, file); err != nil {
		t.Fatal(err)
	}
}
