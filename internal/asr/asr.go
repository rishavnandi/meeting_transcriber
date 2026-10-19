// Package asr turns audio files into transcripts.
//
// Two external programs do the work: ffmpeg decodes the upload into the 16 kHz
// mono PCM WAV the recogniser wants, and nemo-speech recognises it. The
// recogniser is NVIDIA's Nemotron 3.5 ASR running on ggml through
// NeMo-Speech.cpp, which is a fast multilingual model with good Hindi and good
// English.
//
// Two models are used, chosen per recording:
//
//	nemotron-3.5  multilingual, handles English and Hindi in the same file
//	nemotron-en   English only, measurably better on pure English
//
// The choice is made by looking for Devanagari in a short sample of the audio
// before the full transcription starts. See DetectLanguages.
package asr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	// Binary is the nemo-speech executable.
	Binary string
	// FFmpeg and FFprobe decode the upload and measure it.
	FFmpeg  string
	FFprobe string
	// ModelMultilingual and ModelEnglish are the recogniser model names.
	ModelMultilingual string
	ModelEnglish      string
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

// Transcribe runs the recogniser over a WAV file and returns its output.
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

// window is a slice of audio to sample.
type window struct {
	offset float64
	length float64
}

// sampleWindows picks the parts of a recording used to work out which language
// it is in.
//
// The windows are short and spread over the file, because a recording can open
// in English and switch to Hindi halfway through. The extra work is a small
// fraction of a full transcription.
func sampleWindows(duration float64) []window {
	const length = 40
	if duration <= 0 {
		return nil
	}
	if duration <= length {
		return []window{{0, duration}}
	}
	span := duration - length
	switch {
	case duration <= 2*length:
		return []window{{0, length}, {span, length}}
	case duration <= 3*length:
		return []window{{0, length}, {span / 2, length}, {span, length}}
	default:
		return []window{{span * 0.05, length}, {span * 0.45, length}, {span * 0.85, length}}
	}
}

// DetectLanguages samples the recording and reports whether it contains Hindi.
//
// The answer is a boolean rather than a language list on purpose. The question
// that matters is "is there Devanagari in this", because that is what decides
// between the multilingual model and the English-only one. The language names
// the recogniser reports are kept as well, for display.
//
// If not one window could be read the call fails, so the caller can fall back
// to the multilingual model rather than guessing that the audio is English.
func (a *ASR) DetectLanguages(ctx context.Context, wavPath string, duration float64) (devanagari bool, languages []string, err error) {
	windows := sampleWindows(duration)
	if len(windows) == 0 {
		return false, nil, fmt.Errorf("cannot sample a file of %v seconds", duration)
	}
	dir, err := os.MkdirTemp("", "sample-")
	if err != nil {
		return false, nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	seen := map[string]bool{}
	read := 0
	for i, w := range windows {
		clip := filepath.Join(dir, fmt.Sprintf("clip%d.wav", i))
		if err := a.clip(ctx, wavPath, clip, w); err != nil {
			a.log.Warn("language sample failed", "window", i, "err", err)
			continue
		}
		res, err := a.Transcribe(ctx, clip, a.cfg.ModelMultilingual)
		if err != nil {
			a.log.Warn("language sample failed", "window", i, "err", err)
			continue
		}
		read++
		if HasDevanagari(res.Text) {
			devanagari = true
		}
		for _, l := range res.Languages {
			if !seen[l] {
				seen[l] = true
				languages = append(languages, l)
			}
		}
	}
	if read == 0 {
		return false, nil, fmt.Errorf("none of the %d sample windows could be read", len(windows))
	}
	return devanagari, languages, nil
}

// clip cuts one window out of a WAV file.
func (a *ASR) clip(ctx context.Context, src, dst string, w window) error {
	cmd := exec.CommandContext(ctx, a.cfg.FFmpeg,
		"-y", "-loglevel", "error",
		"-ss", strconv.FormatFloat(w.offset, 'f', 3, 64),
		"-i", src, "-t", strconv.FormatFloat(w.length, 'f', 3, 64),
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
		dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg clip: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HasDevanagari reports whether text contains a Devanagari character.
//
// Devanagari is the script Hindi is written in, so its presence is what
// separates a Hindi or Hinglish recording from an English one. The range is
// U+0900 to U+097F.
func HasDevanagari(text string) bool {
	for _, r := range text {
		if r >= 0x0900 && r <= 0x097F {
			return true
		}
	}
	return false
}
