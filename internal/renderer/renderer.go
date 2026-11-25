package renderer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/tnqbao/kubereel/internal/storage"
)

const (
	defaultConcatFPS = 24
	maxCommandStderr = 16 << 10
)

var disallowedFormats = []string{
	"hls",
	"applehttp",
	"concat",
	"dash",
	"rtsp",
	"rtp",
	"sdp",
	"sap",
	"image2",
}

// Renderer runs controlled FFmpeg media operations inside an isolated scratch directory.
type Renderer struct {
	FFmpegPath  string
	FFprobePath string
}

type probedMedia struct {
	localPath string
	width     int
	height    int
	hasVideo  bool
	hasAudio  bool
}

type ffprobeOutput struct {
	Format struct {
		FormatName string `json:"format_name"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

// New returns a Renderer configured to invoke ffmpeg and ffprobe from PATH.
func New() *Renderer {
	return &Renderer{
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
	}
}

// RenderFile reads a JSON render specification from store and writes the rendered MP4 to outputKey.
func (r *Renderer) RenderFile(ctx context.Context, store storage.Store, requestKey, outputKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("artifact storage is required")
	}

	rawRequest, err := readBoundedRequest(ctx, store, requestKey)
	if err != nil {
		return err
	}

	req, err := DecodeRequest(rawRequest)
	if err != nil {
		return err
	}

	return r.Render(ctx, store, req, outputKey)
}

// Render validates req, stages inputs locally, runs FFmpeg without a shell, validates the MP4, and publishes it.
func (r *Renderer) Render(ctx context.Context, store storage.Store, req Request, outputKey string) (renderErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("artifact storage is required")
	}

	if err := ValidateRequest(req); err != nil {
		return err
	}
	if err := ValidateOutputKey(outputKey); err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "kubereel-render-*")
	if err != nil {
		return fmt.Errorf("create render scratch directory: %w", err)
	}
	defer func() {
		renderErr = errors.Join(renderErr, os.RemoveAll(workDir))
	}()

	videoInputs, audioPath, err := r.stageAndProbeInputs(ctx, store, workDir, req)
	if err != nil {
		return err
	}

	targetWidth, targetHeight := resolveTargetDimensions(req, videoInputs[0])
	expectAudio := shouldIncludeAudio(req, videoInputs, audioPath)
	outputPath := filepath.Join(workDir, "output.mp4")

	args := buildFFmpegArgs(req, videoInputs, audioPath, outputPath, targetWidth, targetHeight)
	if err := r.runCommand(ctx, r.ffmpegBinary(), args, "ffmpeg render"); err != nil {
		return err
	}

	if err := r.validateRenderedFile(ctx, outputPath, targetWidth, targetHeight, expectAudio); err != nil {
		return err
	}

	return publishRenderedMP4(ctx, store, outputPath, outputKey)
}

func (r *Renderer) ffmpegBinary() string {
	if r != nil && r.FFmpegPath != "" {
		return r.FFmpegPath
	}

	return "ffmpeg"
}

func (r *Renderer) ffprobeBinary() string {
	if r != nil && r.FFprobePath != "" {
		return r.FFprobePath
	}

	return "ffprobe"
}

func (r *Renderer) stageAndProbeInputs(
	ctx context.Context,
	store storage.Store,
	workDir string,
	req Request,
) ([]probedMedia, string, error) {
	videoInputs := make([]probedMedia, 0, len(req.Inputs))

	for index, key := range req.Inputs {
		localPath := filepath.Join(workDir, fmt.Sprintf("input-%d.media", index))
		if err := stageArtifact(ctx, store, key, localPath); err != nil {
			return nil, "", fmt.Errorf("stage video input %d: %w", index, err)
		}

		probed, err := r.probeFile(ctx, localPath)
		if err != nil {
			return nil, "", fmt.Errorf("probe video input %d: %w", index, err)
		}
		if !probed.hasVideo {
			return nil, "", fmt.Errorf("video input %d contains no video stream", index)
		}

		videoInputs = append(videoInputs, probed)
	}

	if req.AudioInput == "" {
		return videoInputs, "", nil
	}

	audioPath := filepath.Join(workDir, "audio.media")
	if err := stageArtifact(ctx, store, req.AudioInput, audioPath); err != nil {
		return nil, "", fmt.Errorf("stage audio input: %w", err)
	}

	probedAudio, err := r.probeFile(ctx, audioPath)
	if err != nil {
		return nil, "", fmt.Errorf("probe audio input: %w", err)
	}
	if !probedAudio.hasAudio {
		return nil, "", fmt.Errorf("audio input contains no audio stream")
	}

	return videoInputs, audioPath, nil
}

func stageArtifact(ctx context.Context, store storage.Store, key, destPath string) (stageErr error) {
	source, err := store.Open(ctx, key)
	if err != nil {
		return err
	}
	defer func() {
		stageErr = errors.Join(stageErr, source.Close())
	}()

	dest, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if dest != nil {
			stageErr = errors.Join(stageErr, dest.Close())
		}
	}()

	if _, err := io.Copy(dest, source); err != nil {
		return err
	}

	closeErr := dest.Close()
	dest = nil

	return closeErr
}

func (r *Renderer) probeFile(ctx context.Context, localPath string) (probedMedia, error) {
	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		localPath,
	}

	stdout, err := r.runCommandOutput(ctx, r.ffprobeBinary(), args, "ffprobe")
	if err != nil {
		return probedMedia{}, err
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(stdout, &parsed); err != nil {
		return probedMedia{}, fmt.Errorf("decode ffprobe output: %w", err)
	}

	if hasDisallowedFormat(parsed.Format.FormatName) {
		return probedMedia{}, fmt.Errorf("disallowed media container format %q", parsed.Format.FormatName)
	}

	result := probedMedia{localPath: localPath}
	for _, stream := range parsed.Streams {
		if stream.CodecType == "video" && !result.hasVideo && stream.Width > 0 && stream.Height > 0 {
			result.hasVideo = true
			result.width = stream.Width
			result.height = stream.Height
		}
		if stream.CodecType == "audio" {
			result.hasAudio = true
		}
	}

	return result, nil
}

func hasDisallowedFormat(formatList string) bool {
	for _, name := range strings.Split(strings.ToLower(formatList), ",") {
		trimmed := strings.TrimSpace(name)
		if slices.Contains(disallowedFormats, trimmed) {
			return true
		}
	}

	return false
}

func resolveTargetDimensions(req Request, firstInput probedMedia) (int, int) {
	if req.Width > 0 && req.Height > 0 {
		return req.Width, req.Height
	}

	if preset, ok := aspectRatioPresets[req.AspectRatio]; ok {
		return preset[0], preset[1]
	}

	return clampEvenDimension(firstInput.width), clampEvenDimension(firstInput.height)
}

func clampEvenDimension(value int) int {
	even := (value / 2) * 2
	if even < MinDimension {
		return MinDimension
	}
	if even > MaxDimension {
		return MaxDimension
	}

	return even
}

func shouldIncludeAudio(req Request, inputs []probedMedia, audioPath string) bool {
	if audioPath != "" {
		return true
	}

	for _, item := range inputs {
		if !item.hasAudio {
			return false
		}
	}

	return len(inputs) > 0
}

func buildFFmpegArgs(
	req Request,
	inputs []probedMedia,
	audioPath, outputPath string,
	targetWidth, targetHeight int,
) []string {
	args := []string{"-nostdin", "-y", "-v", "error"}

	for index, item := range inputs {
		if index == 0 && len(inputs) == 1 && req.StartSeconds > 0 {
			args = append(args, "-ss", formatSeconds(req.StartSeconds))
		}
		args = append(args, "-protocol_whitelist", "file", "-i", item.localPath)
	}

	if audioPath != "" {
		args = append(args, "-protocol_whitelist", "file", "-i", audioPath)
	}

	if len(inputs) == 1 {
		args = appendSingleInputPipeline(args, req, inputs[0], audioPath, targetWidth, targetHeight)
	} else {
		args = appendConcatPipeline(args, req, inputs, audioPath, targetWidth, targetHeight)
	}

	if req.DurationSeconds > 0 {
		args = append(args, "-t", formatSeconds(req.DurationSeconds))
	}

	args = append(
		args,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		"-f", "mp4",
		outputPath,
	)

	return args
}

func appendSingleInputPipeline(
	args []string,
	req Request,
	input probedMedia,
	audioPath string,
	targetWidth, targetHeight int,
) []string {
	filter := scaleAndPadFilter(targetWidth, targetHeight, req.FPS)
	args = append(args, "-vf", filter, "-map", "0:v:0")

	if audioPath != "" {
		return append(args, "-map", "1:a:0", "-c:a", "aac", "-b:a", "128k", "-shortest")
	}

	if input.hasAudio {
		return append(args, "-map", "0:a:0", "-c:a", "aac", "-b:a", "128k")
	}

	return append(args, "-an")
}

func appendConcatPipeline(
	args []string,
	req Request,
	inputs []probedMedia,
	audioPath string,
	targetWidth, targetHeight int,
) []string {
	fps := req.FPS
	if fps == 0 {
		fps = defaultConcatFPS
	}

	concatAudio := audioPath == "" && shouldIncludeAudio(req, inputs, "")
	filterGraph := buildConcatFilterGraph(inputs, targetWidth, targetHeight, fps, concatAudio)
	args = append(args, "-filter_complex", filterGraph, "-map", "[outv]")

	if audioPath != "" {
		audioIndex := strconv.Itoa(len(inputs))
		return append(args, "-map", audioIndex+":a:0", "-c:a", "aac", "-b:a", "128k", "-shortest")
	}

	if concatAudio {
		return append(args, "-map", "[outa]", "-c:a", "aac", "-b:a", "128k")
	}

	return append(args, "-an")
}

func buildConcatFilterGraph(inputs []probedMedia, width, height, fps int, concatAudio bool) string {
	var builder strings.Builder
	baseFilter := scaleAndPadFilter(width, height, fps)

	for i := range inputs {
		idx := strconv.Itoa(i)
		builder.WriteString("[" + idx + ":v:0]" + baseFilter + "[v" + idx + "];")
		if concatAudio {
			builder.WriteString("[" + idx + ":a:0]aformat=sample_fmts=fltp:sample_rates=44100:channel_layouts=stereo[a" + idx + "];")
		}
	}

	for i := range inputs {
		idx := strconv.Itoa(i)
		builder.WriteString("[v" + idx + "]")
		if concatAudio {
			builder.WriteString("[a" + idx + "]")
		}
	}

	audioFlag := "0"
	outputPads := "[outv]"
	if concatAudio {
		audioFlag = "1"
		outputPads = "[outv][outa]"
	}

	builder.WriteString("concat=n=" + strconv.Itoa(len(inputs)) + ":v=1:a=" + audioFlag + outputPads)
	return builder.String()
}

func scaleAndPadFilter(width, height, fps int) string {
	w := strconv.Itoa(width)
	h := strconv.Itoa(height)
	filter := "scale=" + w + ":" + h + ":force_original_aspect_ratio=decrease,pad=" + w + ":" + h + ":(ow-iw)/2:(oh-ih)/2,setsar=1"
	if fps > 0 {
		filter += ",fps=" + strconv.Itoa(fps)
	}

	return filter + ",format=yuv420p"
}

func formatSeconds(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', 3, 64)
}

func (r *Renderer) validateRenderedFile(
	ctx context.Context,
	outputPath string,
	expectedWidth, expectedHeight int,
	expectAudio bool,
) error {
	file, err := os.Open(outputPath)
	if err != nil {
		return fmt.Errorf("open rendered MP4: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat rendered MP4: %w", err)
	}

	if err := validateMP4Reader(ctx, file, info.Size()); err != nil {
		return fmt.Errorf("validate rendered MP4 container: %w", err)
	}

	return r.verifyRenderedStreams(ctx, outputPath, expectedWidth, expectedHeight, expectAudio)
}

func (r *Renderer) verifyRenderedStreams(
	ctx context.Context,
	outputPath string,
	expectedWidth, expectedHeight int,
	expectAudio bool,
) error {
	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		outputPath,
	}

	stdout, err := r.runCommandOutput(ctx, r.ffprobeBinary(), args, "ffprobe rendered output")
	if err != nil {
		return err
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(stdout, &parsed); err != nil {
		return fmt.Errorf("decode rendered output metadata: %w", err)
	}

	if !strings.Contains(parsed.Format.FormatName, "mp4") {
		return fmt.Errorf("rendered container %q is not MP4", parsed.Format.FormatName)
	}

	hasValidVideo := false
	hasValidAudio := false
	for _, stream := range parsed.Streams {
		if stream.CodecType == "video" && stream.CodecName == "h264" && stream.Width == expectedWidth && stream.Height == expectedHeight {
			hasValidVideo = true
		}
		if stream.CodecType == "audio" && stream.CodecName == "aac" {
			hasValidAudio = true
		}
	}

	if !hasValidVideo {
		return fmt.Errorf("rendered MP4 missing expected %dx%d h264 video stream", expectedWidth, expectedHeight)
	}
	if expectAudio && !hasValidAudio {
		return fmt.Errorf("rendered MP4 missing expected aac audio stream")
	}

	return nil
}

func publishRenderedMP4(ctx context.Context, store storage.Store, localPath, outputKey string) (pubErr error) {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open rendered output: %w", err)
	}
	defer func() {
		pubErr = errors.Join(pubErr, file.Close())
	}()

	return store.Put(ctx, outputKey, file)
}

func (r *Renderer) runCommand(ctx context.Context, binary string, args []string, label string) error {
	_, err := r.runCommandOutput(ctx, binary, args, label)
	return err
}

func (r *Renderer) runCommandOutput(ctx context.Context, binary string, args []string, label string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)

	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	detail := strings.TrimSpace(stderr.String())
	if detail != "" {
		return nil, fmt.Errorf("%s: %w: %s", label, err, detail)
	}

	return nil, fmt.Errorf("%s: %w", label, err)
}

type limitedBuffer struct {
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	totalLen := len(data)
	remaining := maxCommandStderr - b.buf.Len()
	if remaining <= 0 {
		return totalLen, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
	}

	_, _ = b.buf.Write(data)
	return totalLen, nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

func readBoundedRequest(ctx context.Context, store storage.Store, key string) ([]byte, error) {
	object, err := store.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("open render request: %w", err)
	}

	data, readErr := io.ReadAll(io.LimitReader(object, maxRequestBytes+1))
	if err := errors.Join(readErr, object.Close()); err != nil {
		return nil, fmt.Errorf("read render request: %w", err)
	}

	if len(data) > maxRequestBytes {
		return nil, fmt.Errorf("%w: request exceeds %d bytes", ErrInvalidRequest, maxRequestBytes)
	}

	return data, nil
}
