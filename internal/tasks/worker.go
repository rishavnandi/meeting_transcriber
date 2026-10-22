// Package tasks runs the job pipeline: decode, detect the language, pick a
// model, transcribe, summarise.
//
// One job is processed at a time. The recogniser uses every core of the box,
// so running two at once would not make either of them faster, and the box has
// four cores.
package tasks

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"meetingtranscriber/internal/asr"
	"meetingtranscriber/internal/store"
	"meetingtranscriber/internal/summarize"
)

// Worker owns the queue and the pipeline.
type Worker struct {
	st  *store.Store
	asr *asr.ASR
	sum *summarize.Summarizer
	log *slog.Logger

	queue chan string
}

// New builds a worker.
func New(st *store.Store, asrEngine *asr.ASR, sum *summarize.Summarizer, log *slog.Logger) *Worker {
	return &Worker{
		st: st, asr: asrEngine, sum: sum, log: log,
		queue: make(chan string, 64),
	}
}

// Enqueue adds a job to the queue. It never blocks: the queue is deep enough
// for any realistic amount of work, and a full queue means something is badly
// wrong, not that a new upload should be refused.
func (w *Worker) Enqueue(id string) {
	select {
	case w.queue <- id:
	default:
		w.log.Error("queue full", "id", id)
	}
}

// Run consumes the queue until the context is cancelled.
//
// It also picks up jobs that were still pending when the process last stopped,
// so a restart does not lose work.
func (w *Worker) Run(ctx context.Context) {
	jobs, err := w.st.List()
	if err != nil {
		w.log.Error("list jobs at startup", "err", err)
	}
	for _, job := range jobs {
		if job.Status.Pending() {
			w.log.Info("re-queueing pending job", "id", job.ID, "status", job.Status)
			w.Enqueue(job.ID)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-w.queue:
			w.process(ctx, id)
		}
	}
}

// process runs one job from start to finish.
func (w *Worker) process(ctx context.Context, id string) {
	started := time.Now()
	job, err := w.st.Get(id)
	if err != nil {
		w.log.Error("load job", "id", id, "err", err)
		return
	}
	w.log.Info("job started", "id", id, "name", job.Name)

	if err := w.run(ctx, job); err != nil {
		job.Status = store.StatusFailed
		job.Stage = "failed"
		job.Error = err.Error()
		if serr := w.st.Save(job); serr != nil {
			w.log.Error("save failed job", "id", id, "err", serr)
		}
		w.log.Error("job failed", "id", id, "err", err)
		return
	}
	job.Status = store.StatusDone
	job.Stage = "done"
	if err := w.st.Save(job); err != nil {
		w.log.Error("save job", "id", id, "err", err)
	}
	w.log.Info("job done", "id", id, "model", job.Model, "elapsed", time.Since(started))
}

// run is the pipeline. Every step writes the job back to disk so the browser,
// which polls for it, can show where the work has got to.
func (w *Worker) run(ctx context.Context, job *store.Job) error {
	// Step 1: decode the upload into the WAV the recogniser reads.
	w.stage(job, store.StatusDetecting, "decoding the audio")
	wav := w.st.WorkPath(job.ID)
	defer os.Remove(wav)
	if err := w.asr.ToWAV(ctx, w.st.SourcePath(job.ID), wav); err != nil {
		return err
	}
	duration, err := w.asr.Duration(ctx, wav)
	if err != nil {
		return err
	}
	job.Duration = duration

	// Step 2: work out whether the recording is English or Hindi, and pick the
	// model that suits it.
	w.stage(job, store.StatusDetecting, "checking which language the recording is in")
	devanagari, languages, err := w.asr.DetectLanguages(ctx, wav, duration)
	if err != nil {
		// Not fatal. The multilingual model handles both languages, so it is
		// the safe answer when the sample could not be read.
		w.log.Warn("language detection failed, using the multilingual model", "id", job.ID, "err", err)
		devanagari = true
	}
	job.Languages = languages

	// Step 3: transcribe the whole recording with the engine that suits it.
	multilingual, english := w.asr.Models()
	var res *asr.Result
	if devanagari {
		job.Model = multilingual
		w.stage(job, store.StatusTranscribing, "transcribing with "+multilingual)
		res, err = w.asr.Transcribe(ctx, wav, multilingual)
	} else {
		job.Model = english
		w.stage(job, store.StatusTranscribing, "transcribing with "+english)
		res, err = w.asr.TranscribeEnglish(ctx, wav)
	}
	if err != nil {
		return err
	}
	if err := w.writeTranscript(job, res); err != nil {
		return err
	}

	// Step 4: summarise. A summary that fails does not fail the job: the
	// transcript is the reason the recording was uploaded.
	w.stage(job, store.StatusSummarizing, "writing the summary")
	summary, err := w.sum.Summarize(ctx, w.st.TranscriptTextPath(job.ID), job.SummaryModel)
	if err != nil {
		w.log.Error("summary failed", "id", job.ID, "err", err)
		job.SummaryError = err.Error()
		return nil
	}
	job.Summary = summary
	job.SummaryError = ""
	if err := os.WriteFile(w.st.SummaryPath(job.ID), []byte(summary), 0o644); err != nil {
		return err
	}
	return nil
}

// writeTranscript saves the recogniser output in both forms the app needs: the
// raw JSON with word timings, and the readable text the summariser reads.
func (w *Worker) writeTranscript(job *store.Job, res *asr.Result) error {
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(w.st.TranscriptJSONPath(job.ID), raw, 0o644); err != nil {
		return err
	}
	text := res.TranscriptText()
	if err := os.WriteFile(w.st.TranscriptTextPath(job.ID), []byte(text), 0o644); err != nil {
		return err
	}
	job.Languages = res.Languages
	return nil
}

// stage records where the job has got to and saves it.
func (w *Worker) stage(job *store.Job, status store.Status, text string) {
	job.Status = status
	job.Stage = text
	if err := w.st.Save(job); err != nil {
		w.log.Error("save job", "id", job.ID, "err", err)
	}
}
