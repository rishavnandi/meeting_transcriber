// Command meeting_transcriber serves a small web app that turns uploaded
// meeting recordings into transcripts and summaries.
//
// The heavy lifting is done by four programs on the machine rather than by Go:
//
//	ffmpeg      decodes the upload into 16 kHz mono WAV
//	needle      recognises English with Cactus Whistle
//	nemo-speech recognises Hindi with Nemotron 3.5
//	opencode    writes the summary with a free model
//
// The recogniser is chosen per recording: a recording with Hindi in it gets the
// multilingual model, a pure English one gets Whistle, which is roughly fifty
// times faster and gives up some accuracy for it.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"meetingtranscriber/internal/asr"
	"meetingtranscriber/internal/store"
	"meetingtranscriber/internal/summarize"
	"meetingtranscriber/internal/tasks"
	"meetingtranscriber/internal/web"
)

func main() {
	// slog is the stdlib logger. Key-value pairs, not printf format strings.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	// main does nothing but log-and-exit. run returns an error instead of
	// calling os.Exit, because os.Exit skips the deferred cleanup.
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	// NotifyContext cancels the context on Ctrl-C or SIGTERM, which is how the
	// app gets a chance to stop cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.New(env("DATA_DIR", "/data"))
	if err != nil {
		return err
	}

	asrEngine := asr.New(asr.Config{
		Binary:            env("NEMO_BIN", "nemo-speech"),
		Whistle:           env("WHISTLE_BIN", "needle"),
		WhistleModel:      env("WHISTLE_MODEL", "/opt/whistle/whistle.cact"),
		FFmpeg:            env("FFMPEG_BIN", "ffmpeg"),
		FFprobe:           env("FFPROBE_BIN", "ffprobe"),
		ModelMultilingual: env("MODEL_MULTILINGUAL", "nemotron-3.5"),
		ModelEnglish:      env("MODEL_ENGLISH", "whistle"),
	}, log)

	sum := summarize.New(summarize.Config{
		Binary:  env("OPENCODE_BIN", "opencode"),
		Model:   env("SUMMARY_MODEL", "opencode/big-pickle"),
		Timeout: envDuration("SUMMARY_TIMEOUT", 15*time.Minute),
	}, log)

	worker := tasks.New(st, asrEngine, sum, log)
	go worker.Run(ctx)

	srv := &http.Server{
		Addr:    env("ADDR", ":8080"),
		Handler: web.New(st, worker, sum, log),
		// A recording can be a few hundred megabytes, so the read timeout is
		// generous. The size itself is capped by the handler.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Minute,
		// No write timeout: responses are small and an upload in progress must
		// not be cut off.
	}

	go func() {
		log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	return nil
}

// env reads an environment variable, falling back to a default.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envDuration reads a duration from an environment variable.
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
