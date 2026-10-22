// Package asr turns audio files into transcripts.
//
// Three external programs do the work: ffmpeg decodes the upload into the
// 16 kHz mono PCM WAV both recognisers want, needle recognises English with
// Cactus Whistle, and nemo-speech recognises Hindi with NVIDIA Nemotron 3.5.
//
// The language is chosen on the upload form, because the engines disagree on
// what they can hear:
//
//	whistle       English only, 16.9 MB, about fifty times faster than real time
//	nemotron-3.5  multilingual, handles English and Hindi in the same file
//
// Whistle has no Hindi at all, so a recording with any Hindi in it has to go
// to Nemotron. That is why the choice is not detected: a wrong guess would
// produce a confident transcript of the wrong language.
package asr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// Word is one recognised word with its timings in seconds.
type Word struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Result is the recogniser output for one file.
type Result struct {
	Text string `json:"text"`
	// Confidence and Duration are numbers the recogniser reports, and Duration
	// is fractional, so both are floats.
	Confidence float64  `json:"confidence"`
	Duration   float64  `json:"duration"`
	Languages  []string `json:"languages"`
	Words      []Word   `json:"words"`
}

// Config points the package at the external programs it drives.
type Config struct {
	// Binary is the nemo-speech executable, used for Hindi and Hinglish.
	Binary string
	// Whistle is the needle executable and WhistleModel is its weights.
	Whistle      string
	WhistleModel string
	// FFmpeg and FFprobe decode the upload and measure it.
	FFmpeg  string
	FFprobe string
	// ModelMultilingual is the nemo-speech model for Hindi and Hinglish.
	ModelMultilingual string
	// ModelEnglish is the name shown for the English engine.
	ModelEnglish string
	// Threads is how many CPU threads the recogniser may use.
	Threads string
}

// ASR drives the decoder and the recogniser.
type ASR struct {
	cfg Config
	log *slog.Logger
}

// New builds an ASR.
func New(cfg Config, log *slog.Logger) *ASR {
	return &ASR{cfg: cfg, log: log}
}

// Models returns the two model names, so the caller can show them.
func (a *ASR) Models() (multilingual, english string) {
	return a.cfg.ModelMultilingual, a.cfg.ModelEnglish
}

// ToWAV decodes any audio file into 16 kHz mono 16-bit PCM.
//
// The recogniser only reads WAV, so this has to happen for every upload
// whatever it arrived as.
func (a *ASR) ToWAV(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, a.cfg.FFmpeg,
		"-y", "-loglevel", "error",
		"-i", src,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
		dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Duration measures a media file in seconds.
func (a *ASR) Duration(ctx context.Context, path string) (float64, error) {
	cmd := exec.CommandContext(ctx, a.cfg.FFprobe,
		"-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe gave %q: %w", strings.TrimSpace(string(out)), err)
	}
	return d, nil
}

// Transcribe runs Nemotron over a WAV file and returns its output.
//
// This is the multilingual recogniser, used when the recording contains Hindi.
func (a *ASR) Transcribe(ctx context.Context, wavPath, model string) (*Result, error) {
	args := []string{"transcribe", wavPath, "--model", model, "--format", "json", "--quiet"}
	cmd := exec.CommandContext(ctx, a.cfg.Binary, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("nemo-speech: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var res Result
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("decode recogniser output: %w", err)
	}
	return &res, nil
}

// whistleResult is what the needle engine prints for one file.
type whistleResult struct {
	Text     string `json:"text"`
	Language string `json:"language"`
	Words    []struct {
		Word        string  `json:"word"`
		Start       float64 `json:"start"`
		End         float64 `json:"end"`
		Probability float64 `json:"probability"`
	} `json:"words"`
}

// TranscribeEnglish runs Whistle over a WAV file and returns its output.
//
// Whistle is English only, and much faster than Nemotron, so it gets every
// recording the uploader marked as English. The language is forced because
// that call has already been made.
func (a *ASR) TranscribeEnglish(ctx context.Context, wavPath string) (*Result, error) {
	cmd := exec.CommandContext(ctx, a.cfg.Whistle,
		"--model", a.cfg.WhistleModel,
		"--audio", wavPath,
		"--audio-language", "en",
		"--audio-word-timestamps")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("needle: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var wr whistleResult
	if err := json.Unmarshal(out, &wr); err != nil {
		return nil, fmt.Errorf("decode whistle output: %w", err)
	}
	res := &Result{Text: wr.Text}
	if wr.Language != "" {
		res.Languages = []string{wr.Language}
	}
	for _, w := range wr.Words {
		if w.Word == "" {
			continue
		}
		res.Words = append(res.Words, Word{Word: w.Word, Start: w.Start, End: w.End})
	}
	return res, nil
}
