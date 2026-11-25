// Package renderer executes controlled, validated FFmpeg media operations.
package renderer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"slices"
	"strings"
)

const (
	OpMerge       = "merge"
	OpTrim        = "trim"
	OpResize      = "resize"
	OpAspectRatio = "aspect_ratio"
	OpAddAudio    = "add_audio"
	OpNormalize   = "normalize"
	OpRenderMP4   = "render_mp4"

	MaxInputs          = 32
	MinDimension       = 16
	MaxDimension       = 3840
	MinDurationSeconds = 0.1
	MaxDurationSeconds = 86400.0
	MinFPS             = 1
	MaxFPS             = 60

	maxKeyLength    = 253
	maxRequestBytes = 1 << 20
)

// ErrInvalidRequest indicates that a render specification failed validation.
var ErrInvalidRequest = errors.New("invalid render request")

var (
	supportedOperations = []string{
		OpMerge,
		OpTrim,
		OpResize,
		OpAspectRatio,
		OpAddAudio,
		OpNormalize,
		OpRenderMP4,
	}

	allowedVideoExtensions = []string{
		".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v",
	}

	allowedAudioExtensions = []string{
		".mp3", ".m4a", ".aac", ".wav", ".ogg", ".flac",
		".mp4", ".mov", ".mkv", ".webm",
	}

	aspectRatioPresets = map[string][2]int{
		"16:9": {1280, 720},
		"9:16": {720, 1280},
		"1:1":  {720, 720},
		"4:3":  {960, 720},
		"4:5":  {720, 900},
	}
)

// Request defines a controlled FFmpeg media processing operation.
type Request struct {
	Operation       string   `json:"operation"`
	Inputs          []string `json:"inputs"`
	AudioInput      string   `json:"audioInput,omitempty"`
	Width           int      `json:"width,omitempty"`
	Height          int      `json:"height,omitempty"`
	AspectRatio     string   `json:"aspectRatio,omitempty"`
	StartSeconds    float64  `json:"startSeconds,omitempty"`
	DurationSeconds float64  `json:"durationSeconds,omitempty"`
	FPS             int      `json:"fps,omitempty"`
}

// DecodeRequest parses a strict JSON render request and validates all fields.
func DecodeRequest(data []byte) (Request, error) {
	if len(data) == 0 || len(data) > maxRequestBytes {
		return Request{}, fmt.Errorf("%w: request must be between 1 and %d bytes", ErrInvalidRequest, maxRequestBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var req Request
	if err := decoder.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalidRequest, err)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Request{}, fmt.Errorf("%w: unexpected trailing JSON data", ErrInvalidRequest)
	}

	if err := ValidateRequest(req); err != nil {
		return Request{}, err
	}

	return req, nil
}

// ValidateRequest checks that a render specification uses only safe, supported parameters.
func ValidateRequest(req Request) error {
	if !slices.Contains(supportedOperations, req.Operation) {
		return fmt.Errorf("%w: unsupported operation %q", ErrInvalidRequest, req.Operation)
	}

	if err := validateInputs(req); err != nil {
		return err
	}

	if err := validateAudioInput(req); err != nil {
		return err
	}

	if err := validateDimensions(req); err != nil {
		return err
	}

	if err := validateTiming(req); err != nil {
		return err
	}

	if req.FPS < 0 || (req.FPS != 0 && (req.FPS < MinFPS || req.FPS > MaxFPS)) {
		return fmt.Errorf("%w: fps must be between %d and %d", ErrInvalidRequest, MinFPS, MaxFPS)
	}

	return nil
}

func validateInputs(req Request) error {
	count := len(req.Inputs)
	if count == 0 || count > MaxInputs {
		return fmt.Errorf("%w: inputs must contain 1 to %d items", ErrInvalidRequest, MaxInputs)
	}

	if req.Operation == OpMerge && count < 2 {
		return fmt.Errorf("%w: merge requires at least 2 inputs", ErrInvalidRequest)
	}

	if req.Operation != OpMerge && req.Operation != OpRenderMP4 && count != 1 {
		return fmt.Errorf("%w: %s requires exactly 1 video input", ErrInvalidRequest, req.Operation)
	}

	for _, key := range req.Inputs {
		if err := ValidateVideoKey(key); err != nil {
			return err
		}
	}

	return nil
}

func validateAudioInput(req Request) error {
	if req.Operation == OpAddAudio && req.AudioInput == "" {
		return fmt.Errorf("%w: add_audio requires audioInput", ErrInvalidRequest)
	}

	if req.Operation != OpAddAudio && req.Operation != OpRenderMP4 && req.AudioInput != "" {
		return fmt.Errorf("%w: audioInput is not supported for %s", ErrInvalidRequest, req.Operation)
	}

	if req.AudioInput == "" {
		return nil
	}

	return ValidateAudioKey(req.AudioInput)
}

func validateDimensions(req Request) error {
	hasSize := req.Width != 0 || req.Height != 0
	hasRatio := req.AspectRatio != ""

	if hasSize && hasRatio {
		return fmt.Errorf("%w: width/height and aspectRatio are mutually exclusive", ErrInvalidRequest)
	}

	if req.Operation == OpResize && !hasSize {
		return fmt.Errorf("%w: resize requires width and height", ErrInvalidRequest)
	}

	if req.Operation == OpAspectRatio && !hasRatio {
		return fmt.Errorf("%w: aspect_ratio requires aspectRatio", ErrInvalidRequest)
	}

	allowsSize := req.Operation == OpResize || req.Operation == OpMerge || req.Operation == OpRenderMP4
	if hasSize && !allowsSize {
		return fmt.Errorf("%w: width and height are not supported for %s", ErrInvalidRequest, req.Operation)
	}

	allowsRatio := req.Operation == OpAspectRatio || req.Operation == OpRenderMP4
	if hasRatio && !allowsRatio {
		return fmt.Errorf("%w: aspectRatio is not supported for %s", ErrInvalidRequest, req.Operation)
	}

	if hasSize && !isValidEvenDimensionPair(req.Width, req.Height) {
		return fmt.Errorf("%w: width and height must be even integers between %d and %d", ErrInvalidRequest, MinDimension, MaxDimension)
	}

	if _, ok := aspectRatioPresets[req.AspectRatio]; hasRatio && !ok {
		return fmt.Errorf("%w: unsupported aspectRatio %q", ErrInvalidRequest, req.AspectRatio)
	}

	return nil
}

func isValidEvenDimensionPair(width, height int) bool {
	if width < MinDimension || width > MaxDimension || width%2 != 0 {
		return false
	}

	if height < MinDimension || height > MaxDimension || height%2 != 0 {
		return false
	}

	return true
}

func validateTiming(req Request) error {
	if !isFiniteRange(req.StartSeconds, 0, MaxDurationSeconds) {
		return fmt.Errorf("%w: startSeconds must be between 0 and %.0f", ErrInvalidRequest, MaxDurationSeconds)
	}

	if !isFiniteRange(req.DurationSeconds, 0, MaxDurationSeconds) {
		return fmt.Errorf("%w: durationSeconds must be between 0 and %.0f", ErrInvalidRequest, MaxDurationSeconds)
	}

	hasTiming := req.StartSeconds > 0 || req.DurationSeconds > 0
	allowsTiming := req.Operation == OpTrim || req.Operation == OpRenderMP4
	if hasTiming && !allowsTiming {
		return fmt.Errorf("%w: trim timing is not supported for %s", ErrInvalidRequest, req.Operation)
	}

	if req.Operation == OpTrim && req.DurationSeconds < MinDurationSeconds {
		return fmt.Errorf("%w: trim durationSeconds must be at least %.1f", ErrInvalidRequest, MinDurationSeconds)
	}

	if req.DurationSeconds > 0 && req.DurationSeconds < MinDurationSeconds {
		return fmt.Errorf("%w: durationSeconds must be at least %.1f", ErrInvalidRequest, MinDurationSeconds)
	}

	return nil
}

func isFiniteRange(value, minValue, maxValue float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}

	return value >= minValue && value <= maxValue
}

// ValidateVideoKey checks that key is a safe relative path with a supported video extension.
func ValidateVideoKey(key string) error {
	return validateMediaKey(key, allowedVideoExtensions, "video")
}

// ValidateAudioKey checks that key is a safe relative path with a supported audio extension.
func ValidateAudioKey(key string) error {
	return validateMediaKey(key, allowedAudioExtensions, "audio")
}

// ValidateOutputKey checks that key is a safe relative path ending in .mp4.
func ValidateOutputKey(key string) error {
	if err := validateSafeKey(key); err != nil {
		return err
	}

	if path.Ext(key) != ".mp4" {
		return fmt.Errorf("%w: output artifact %q must have a .mp4 extension", ErrInvalidRequest, key)
	}

	return nil
}

func validateMediaKey(key string, allowedExts []string, kind string) error {
	if err := validateSafeKey(key); err != nil {
		return err
	}

	ext := strings.ToLower(path.Ext(key))
	if !slices.Contains(allowedExts, ext) {
		return fmt.Errorf("%w: %s input %q has unsupported extension", ErrInvalidRequest, kind, key)
	}

	return nil
}

func validateSafeKey(key string) error {
	if key == "" || key == "." || len(key) > maxKeyLength || !fs.ValidPath(key) || strings.ContainsAny(key, "\\\x00") {
		return fmt.Errorf("%w: invalid artifact key %q", ErrInvalidRequest, key)
	}

	for _, segment := range strings.Split(key, "/") {
		if strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "-") {
			return fmt.Errorf("%w: disallowed segment in artifact key %q", ErrInvalidRequest, key)
		}
	}

	return nil
}
