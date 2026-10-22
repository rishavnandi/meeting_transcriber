// Package web serves the htmx UI.
//
// Every route answers with HTML. htmx swaps the returned fragment into the
// page, so the handlers never redirect and never return JSON. The one
// exception is the two download routes, which return plain files.
package web

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"meetingtranscriber/internal/store"
	"meetingtranscriber/internal/summarize"
	"meetingtranscriber/internal/tasks"
)

// Embeds every asset into the binary, so the container ships one file.
//
//go:embed templates/*.html static/*
var files embed.FS

// maxUpload is the largest recording the app accepts. Two hours of MP3 at a
// typical bitrate is well under this.
const maxUpload = 500 << 20

// Server holds the app's dependencies.
type Server struct {
	st     *store.Store
	worker *tasks.Worker
	sum    *summarize.Summarizer
	log    *slog.Logger
	tmpl   *template.Template
}

// New wires the app together and returns the root handler.
func New(st *store.Store, worker *tasks.Worker, sum *summarize.Summarizer, log *slog.Logger) http.Handler {
	s := &Server{st: st, worker: worker, sum: sum, log: log,
		tmpl: template.Must(template.New("t").Funcs(template.FuncMap{
			"clock": func(seconds float64) string { return formatClock(seconds) },
			"bytes": func(n int64) string { return formatBytes(n) },
			// dict builds a one-key map so a partial can be rendered from inside
			// a range loop, where the dot is the loop value rather than the view.
			"dict": func(kv ...any) map[string]any {
				m := map[string]any{}
				for i := 0; i+1 < len(kv); i += 2 {
					if k, ok := kv[i].(string); ok {
						m[k] = kv[i+1]
					}
				}
				return m
			},
		}).ParseFS(files, "templates/*.html"))}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /jobs", s.upload)
	mux.HandleFunc("GET /jobs/{id}", s.card)
	mux.HandleFunc("GET /jobs/{id}/transcript", s.transcript)
	mux.HandleFunc("GET /jobs/{id}/transcript.txt", s.downloadTranscript)
	mux.HandleFunc("GET /jobs/{id}/summary.md", s.downloadSummary)
	mux.HandleFunc("DELETE /jobs/{id}", s.delete)
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	static, _ := fs.Sub(files, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		log.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}

// view is what every template gets.
type view struct {
	Title string
	Jobs  []*store.Job
	Job   *store.Job
	// Models is the list of free models the summary can be written with.
	Models []summarize.Model
	// DefaultModel is the model used when the picker is not there.
	DefaultModel string
	// Summary is the generated summary, already rendered from markdown.
	Summary template.HTML
	// Transcript is the timestamped transcript.
	Transcript string
	Err        string
}

// index is the home page: the upload form and the list of recordings.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.st.List()
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.render(w, http.StatusOK, "index", view{Title: "Meeting transcriber", Jobs: jobs})
}

// upload takes a recording and queues it.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("read the upload: %w", err))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("no file in the upload: %w", err))
		return
	}
	defer file.Close()

	id, err := newID()
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	// Anything but Hindi is English. The form only offers those two.
	language := r.FormValue("language")
	if language != "hi" {
		language = "en"
	}
	job, err := s.st.Create(id, filepathSafe(header.Filename), header.Size, file, r.FormValue("summary_model"), language)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.worker.Enqueue(id)
	s.log.Info("uploaded", "id", id, "name", job.Name, "bytes", job.SizeBytes,
		"language", job.Language, "summary_model", job.SummaryModel)

	// The card comes back as a fragment and htmx puts it at the top of the
	// list, so the recording appears without a page reload.
	s.render(w, http.StatusCreated, "job_card", view{Job: job})
}

// models is the picker for the model that writes the summary.
//
// It is a fragment that htmx loads into the upload form, so the list of free
// models is fetched when the page opens and not baked into the binary. The
// free models come and go, so the list has to be live.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	models, err := s.sum.FreeModels(r.Context())
	if err != nil {
		s.log.Error("free models", "err", err)
		// The form still works without a picker: the default model is used.
		s.render(w, http.StatusOK, "model_picker", view{Err: "the list of free models could not be loaded"})
		return
	}
	s.render(w, http.StatusOK, "model_picker", view{Models: models, DefaultModel: s.sum.DefaultModel()})
}

// card is the fragment for one job. The browser polls this while the job is
// still running.
func (s *Server) card(w http.ResponseWriter, r *http.Request) {
	job, err := s.st.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "no such job", http.StatusNotFound)
			return
		}
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.render(w, http.StatusOK, "job_card", view{Job: job})
}

// transcript is the full page for one recording: the summary and the
// transcript.
func (s *Server) transcript(w http.ResponseWriter, r *http.Request) {
	job, err := s.st.Get(r.PathValue("id"))
	if err != nil {
		s.fail(w, http.StatusNotFound, err)
		return
	}
	text, err := os.ReadFile(s.st.TranscriptTextPath(job.ID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.render(w, http.StatusOK, "transcript", view{
		Title:      job.Name,
		Job:        job,
		Summary:    renderMarkdown(job.Summary),
		Transcript: string(text),
	})
}

// downloadTranscript sends the transcript as a text file.
func (s *Server) downloadTranscript(w http.ResponseWriter, r *http.Request) {
	s.sendFile(w, r, s.st.TranscriptTextPath(r.PathValue("id")), ".txt", "text/plain; charset=utf-8")
}

// downloadSummary sends the summary as a markdown file.
func (s *Server) downloadSummary(w http.ResponseWriter, r *http.Request) {
	s.sendFile(w, r, s.st.SummaryPath(r.PathValue("id")), ".md", "text/markdown; charset=utf-8")
}

// sendFile writes a file from the data directory to the browser.
func (s *Server) sendFile(w http.ResponseWriter, r *http.Request, path, suffix, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not ready yet", http.StatusNotFound)
			return
		}
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	name := r.PathValue("id")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+suffix+"\"")
	_, _ = io.Copy(w, f)
}

// delete removes a recording and its results.
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Delete(r.PathValue("id")); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// render writes a template to the response. The render goes into a buffer
// first, so a template that fails halfway cannot emit half a page with a 200
// status.
func (s *Server) render(w http.ResponseWriter, code int, name string, v view) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, v); err != nil {
		s.log.Error("template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// fail logs the real error and sends a plain message. The detail belongs in
// the log, not in a browser.
func (s *Server) fail(w http.ResponseWriter, code int, err error) {
	s.log.Error("request failed", "err", err)
	http.Error(w, err.Error(), code)
}

// newID makes a short random identifier for a job.
func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// filepathSafe keeps only the last path element of an uploaded file name and
// strips anything that is not a letter, a digit or a dot. The name is only ever
// shown in the browser, but a name from the internet still gets treated as one.
func filepathSafe(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "recording"
	}
	return out
}

// formatBytes renders a file size for the browser.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// formatClock renders a duration in seconds as mm:ss, or h:mm:ss past an hour.
func formatClock(seconds float64) string {
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
