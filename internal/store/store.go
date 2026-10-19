// Package store keeps job state and the files each job owns.
//
// There is no database. Every job is a directory under the data root, and the
// job itself is one JSON file in it. A single user drops a few recordings a
// week, so a directory listing is a perfectly good index and it keeps the
// container free of a second service to keep alive.
//
// Layout under the data root:
//
//	jobs/<id>/job.json         the job: status, stage, summary
//	jobs/<id>/source.mp3       the uploaded recording, byte for byte
//	jobs/<id>/transcript.json  the raw recogniser output, including word times
//	jobs/<id>/transcript.txt   the same transcript as readable timestamped text
//	jobs/<id>/summary.md       the generated summary
//	work/<id>.wav              the decoded 16 kHz mono audio, deleted when done
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Status is where a job is in the pipeline.
type Status string

const (
	StatusQueued       Status = "queued"
	StatusDetecting    Status = "detecting"
	StatusTranscribing Status = "transcribing"
	StatusSummarizing  Status = "summarizing"
	StatusDone         Status = "done"
	StatusFailed       Status = "failed"
)

// Pending reports whether the job still needs work. The worker uses it to
// decide what to do with a job it picks up.
func (s Status) Pending() bool {
	switch s {
	case StatusQueued, StatusDetecting, StatusTranscribing, StatusSummarizing:
		return true
	}
	return false
}

// Job is everything the UI needs to show a recording and its results.
type Job struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	Status    Status    `json:"status"`
	Stage     string    `json:"stage"`
	Error     string    `json:"error"`
	Model     string    `json:"model"`
	Languages []string  `json:"languages"`
	Duration  float64   `json:"duration_seconds"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Summary   string    `json:"summary,omitempty"`
	// SummaryError is set when the transcript worked but the summary did not.
	// The job still succeeds, because the transcript is the point.
	SummaryError string `json:"summary_error,omitempty"`
	SourcePath   string `json:"-"`
}

// Store reads and writes jobs under a data root.
type Store struct {
	root string
}

// New creates the data root if it does not exist yet.
func New(root string) (*Store, error) {
	for _, dir := range []string{"jobs", "work"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return &Store{root: root}, nil
}

func (s *Store) jobDir(id string) string { return filepath.Join(s.root, "jobs", id) }

// JobPath is the JSON file holding a job.
func (s *Store) JobPath(id string) string { return filepath.Join(s.jobDir(id), "job.json") }

// SourcePath is the uploaded recording for a job.
func (s *Store) SourcePath(id string) string { return filepath.Join(s.jobDir(id), "source.mp3") }

// TranscriptJSONPath is the raw recogniser output.
func (s *Store) TranscriptJSONPath(id string) string {
	return filepath.Join(s.jobDir(id), "transcript.json")
}

// TranscriptTextPath is the readable transcript handed to the summariser.
func (s *Store) TranscriptTextPath(id string) string {
	return filepath.Join(s.jobDir(id), "transcript.txt")
}

// SummaryPath is the generated summary.
func (s *Store) SummaryPath(id string) string {
	return filepath.Join(s.jobDir(id), "summary.md")
}

// WorkPath is the scratch WAV for a job. It is removed once the job is done.
func (s *Store) WorkPath(id string) string {
	return filepath.Join(s.root, "work", id+".wav")
}

// Create writes a new job and its source file. The recording is copied rather
// than moved so the upload can be streamed straight to disk.
func (s *Store) Create(id, name string, size int64, src io.Reader) (*Job, error) {
	dir := s.jobDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create job dir: %w", err)
	}
	dst, err := os.Create(s.SourcePath(id))
	if err != nil {
		return nil, fmt.Errorf("create source: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return nil, fmt.Errorf("save source: %w", err)
	}
	if err := dst.Close(); err != nil {
		return nil, fmt.Errorf("close source: %w", err)
	}
	now := time.Now().UTC()
	job := &Job{
		ID: id, Name: name, SizeBytes: size,
		Status: StatusQueued, Stage: "waiting for a free worker",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Save(job); err != nil {
		return nil, err
	}
	return job, nil
}

// Save writes a job back to disk.
func (s *Store) Save(job *Job) error {
	job.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	path := s.JobPath(job.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write job: %w", err)
	}
	// Rename is atomic on the same filesystem, so a reader never sees a
	// half-written job.
	return os.Rename(tmp, path)
}

// Get loads one job.
func (s *Store) Get(id string) (*Job, error) {
	b, err := os.ReadFile(s.JobPath(id))
	if err != nil {
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(b, &job); err != nil {
		return nil, fmt.Errorf("decode job: %w", err)
	}
	job.SourcePath = s.SourcePath(id)
	return &job, nil
}

// List returns every job, newest first.
func (s *Store) List() ([]*Job, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "jobs"))
	if err != nil {
		return nil, fmt.Errorf("read jobs: %w", err)
	}
	var jobs []*Job
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		job, err := s.Get(e.Name())
		if err != nil {
			// A job directory without a readable job.json is not worth failing
			// the whole listing over.
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	return jobs, nil
}

// Delete removes a job and everything it owns.
func (s *Store) Delete(id string) error {
	if err := os.RemoveAll(s.jobDir(id)); err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	if err := os.Remove(s.WorkPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete work file: %w", err)
	}
	return nil
}
