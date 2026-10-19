// Package summarize generates a meeting summary from a transcript.
//
// The summary comes from OpenCode run against a free OpenCode Zen model, so it
// needs no API key and costs nothing. big-pickle is used because it is a free
// model that stays available, unlike the other free models that come and go.
//
//	opencode run --model opencode/big-pickle "<prompt>" --file transcript.txt
//
// OpenCode is an agent, so it prints its reasoning as it works. The --format
// json flag turns that into one JSON object per line, and this package takes
// the text of the final message.
package summarize

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config points the package at the OpenCode binary and the model to use.
type Config struct {
	// Binary is the opencode executable.
	Binary string
	// Model is the provider/model to run, such as opencode/big-pickle.
	Model string
	// Timeout bounds one summary. A long transcript can take a few minutes.
	Timeout time.Duration
}

// Summarizer runs the summariser.
type Summarizer struct {
	cfg Config
	log *slog.Logger

	// The free model list, and when it was fetched.
	mu       sync.Mutex
	cached   []Model
	cachedAt time.Time
}

// New builds a Summarizer.
func New(cfg Config, log *slog.Logger) *Summarizer {
	return &Summarizer{cfg: cfg, log: log}
}

// prompt is the instruction sent with the transcript.
//
// It asks for the four sections a meeting note needs, and it says not to invent
// facts, which matters because the model is asked to work from a transcript
// that may be incomplete.
const prompt = `Summarize this meeting transcript.

Reply with markdown that has exactly these sections:

## Summary
Three to five sentences on what the meeting was about and what was concluded.

## Key decisions
One bullet per decision that was actually made. Leave this section out if nothing was decided.

## Action items
A markdown table with the columns Owner | Action | Due. Use only owners named in the transcript. Put nothing in Due if the transcript does not give one.

## Open questions
One bullet per question the meeting did not answer.

Write in the same mix of languages the transcript uses. Keep names, products and technical terms in Latin script. Do not invent anything that is not in the transcript.`

// partPrompt is the instruction for one part of a meeting that is too long to
// read in one go.
const partPrompt = `This is part %d of %d of a single meeting transcript. Summarize this part on its own.

Return markdown with the same sections: ## Summary, ## Key decisions, ## Action items (a markdown table with the columns Owner | Action | Due), ## Open questions.

Write in the same mix of languages the transcript uses. Keep names, products and technical terms in Latin script. Do not invent anything that is not in the transcript. Do not say that this is part of a longer meeting, just summarize what is in front of you.`

// mergePrompt is the instruction that folds the part summaries into one.
const mergePrompt = `Below are the summaries of each part of one meeting, in order, separated by ---. Merge them into a single summary of the whole meeting.

Return markdown with exactly these sections:

## Summary
Three to five sentences on what the meeting was about and what was concluded.

## Key decisions
One bullet per decision that was actually made. Leave this section out if nothing was decided.

## Action items
A markdown table with the columns Owner | Action | Due. Combine the action items from all the parts into one table and drop duplicates. Use only owners named in the parts. Put nothing in Due if the parts do not give one.

## Open questions
One bullet per question the meeting did not answer.

Write in the same mix of languages the parts use. Keep names, products and technical terms in Latin script. Do not invent anything that is not in the parts.`

// Model is a model the summary can be written with.
type Model struct {
	// ID is the provider/model name, such as opencode/big-pickle.
	ID string `json:"id"`
	// Name is the model's display name.
	Name string `json:"name"`
	// Context is how many tokens the model can take.
	Context int `json:"context"`
}

// catalogURL is where the model catalog with prices comes from.
const catalogURL = "https://models.dev/api.json"

// catalogTTL is how long the model list is kept before it is fetched again.
// The free models change rarely, and the catalog is a large download.
const catalogTTL = 10 * time.Minute

// DefaultModel is the model used when a job does not name one.
func (s *Summarizer) DefaultModel() string { return s.cfg.Model }

// FreeModels returns the free OpenCode Zen models available right now.
//
// The live list comes from the opencode binary, which knows what the account
// can actually reach. The free flag comes from the catalog, which knows what
// each model costs. A model has to be in both to be offered, so a model that
// is free but not served, or served but not free, is left out.
//
// The result is cached, because the catalog is a large download and the list
// changes rarely.
func (s *Summarizer) FreeModels(ctx context.Context) ([]Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.cachedAt) < catalogTTL && s.cached != nil {
		return s.cached, nil
	}

	live, err := s.liveModels(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}

	var models []Model
	for id, entry := range catalog {
		if !live[id] {
			continue
		}
		if entry.Cost.Input != 0 || entry.Cost.Output != 0 {
			continue
		}
		name := entry.Name
		if name == "" {
			name = id
		}
		models = append(models, Model{ID: id, Name: name, Context: entry.Limit.Context})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	s.cached, s.cachedAt = models, time.Now()
	return models, nil
}

// liveModels asks the opencode binary which models are available.
func (s *Summarizer) liveModels(ctx context.Context) (map[string]bool, error) {
	cmd := exec.CommandContext(ctx, s.cfg.Binary, "models")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode models: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	live := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// The lines look like "provider/model  name  ...". Only the Zen
		// provider is free, so only its models are of interest.
		if !strings.HasPrefix(fields[0], "opencode/") {
			continue
		}
		live[fields[0]] = true
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("opencode models listed no opencode models")
	}
	return live, nil
}

// catalogEntry is one model in the catalog.
type catalogEntry struct {
	Name string `json:"name"`
	Cost struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
}

// catalog fetches the model catalog, keyed by provider/model name.
func (s *Summarizer) catalog(ctx context.Context) (map[string]catalogEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch the model catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch the model catalog: %s", resp.Status)
	}
	var all struct {
		Opencode struct {
			Models map[string]catalogEntry `json:"models"`
		} `json:"opencode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, fmt.Errorf("decode the model catalog: %w", err)
	}
	if len(all.Opencode.Models) == 0 {
		return nil, fmt.Errorf("the model catalog has no opencode models")
	}
	out := make(map[string]catalogEntry, len(all.Opencode.Models))
	for id, entry := range all.Opencode.Models {
		out["opencode/"+id] = entry
	}
	return out, nil
}

// event is one line of OpenCode's JSON output.
type event struct {
	Type string `json:"type"`
	Part struct {
		MessageID string `json:"messageID"`
		Text      string `json:"text"`
	} `json:"part"`
}

// chunkLines is how many lines of transcript go into one model call.
//
// The model reads the transcript with a tool that returns at most 2000 lines
// by default. Anything past that is never read, and the model does not notice:
// it answers as if it had seen the whole file. So a chunk has to stay under
// that line, and a long meeting is summarised a part at a time.
const chunkLines = 1500

// chunkBytes caps the size of a chunk, for the case where a handful of very
// long lines carry a lot of text.
const chunkBytes = 200_000

// Summarize reads a transcript file and returns the summary as markdown.
//
// The model is the one chosen for this job. An empty model means the default.
//
// A short meeting is summarised in one call. A long one is split into parts,
// each part is summarised on its own, and the part summaries are then merged
// into one. That keeps every call inside what the model's read tool can
// return, so no part of the meeting is silently left out.
func (s *Summarizer) Summarize(ctx context.Context, transcriptPath, model string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	if model == "" {
		model = s.cfg.Model
	}

	chunks, err := splitTranscript(transcriptPath)
	if err != nil {
		return "", err
	}
	if len(chunks) <= 1 {
		return s.run(ctx, transcriptPath, model, prompt)
	}

	// Part summaries, in order.
	partials := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		part, err := s.part(ctx, transcriptPath, model, chunk, i+1, len(chunks))
		if err != nil {
			return "", err
		}
		partials = append(partials, part)
	}

	// Merge the part summaries into one.
	merged := strings.Join(partials, "\n\n---\n\n")
	path, cleanup, err := writeTemp(transcriptPath, ".parts.md", merged)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return s.run(ctx, path, model, mergePrompt)
}

// part summarises one chunk of a long meeting.
func (s *Summarizer) part(ctx context.Context, transcriptPath, model, chunk string, index, total int) (string, error) {
	path, cleanup, err := writeTemp(transcriptPath, fmt.Sprintf(".part%d.md", index), chunk)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return s.run(ctx, path, model, fmt.Sprintf(partPrompt, index, total))
}

// run makes one call to OpenCode and returns the text of its answer.
func (s *Summarizer) run(ctx context.Context, path, model, instruction string) (string, error) {
	// The message goes before --file. --file takes a list of values, so a
	// message placed after it would be swallowed as another filename.
	cmd := exec.CommandContext(ctx, s.cfg.Binary,
		"run", "--model", model, "--format", "json",
		instruction, "--file", path)
	cmd.Dir = path[:strings.LastIndex(path, "/")]

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("opencode run: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	text := lastMessage(out)
	if text == "" {
		return "", fmt.Errorf("opencode run produced no answer: %s", strings.TrimSpace(stderr.String()))
	}
	return text, nil
}

// splitTranscript reads a transcript and cuts it into chunks.
//
// A chunk ends at whichever comes first: chunkLines lines, or chunkBytes of
// text. A line longer than the byte cap is cut on its own rather than being
// dropped.
func splitTranscript(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	lines := strings.Split(string(b), "\n")

	var chunks []string
	var cur []string
	curBytes := 0
	for _, line := range lines {
		// A line longer than the byte cap is cut into pieces of its own, so
		// that no chunk can end up over the cap.
		for len(line) > chunkBytes {
			if len(cur) > 0 {
				chunks = append(chunks, strings.Join(cur, "\n"))
				cur, curBytes = nil, 0
			}
			chunks = append(chunks, line[:chunkBytes])
			line = line[chunkBytes:]
		}
		if len(cur) > 0 && (len(cur) >= chunkLines || curBytes+len(line) > chunkBytes) {
			chunks = append(chunks, strings.Join(cur, "\n"))
			cur, curBytes = nil, 0
		}
		cur = append(cur, line)
		curBytes += len(line) + 1
	}
	if len(cur) > 0 {
		chunks = append(chunks, strings.Join(cur, "\n"))
	}
	return chunks, nil
}

// writeTemp writes content next to the transcript and returns the path and a
// function that removes it. The file goes in the same directory so that the
// paths OpenCode reports make sense to it.
func writeTemp(transcriptPath, name, content string) (string, func(), error) {
	dir := transcriptPath[:strings.LastIndex(transcriptPath, "/")]
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", func() {}, fmt.Errorf("write %s: %w", name, err)
	}
	return path, func() { os.Remove(path) }, nil
}

// lastMessage pulls the text of the final assistant message out of OpenCode's
// JSON lines.
//
// The agent can emit several messages: a thought, a tool call, then the answer.
// The last message is the answer, so the text of that message is kept and
// anything before it is dropped.
func lastMessage(out []byte) string {
	var lastID, text string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "text" {
			continue
		}
		// A new message id means a new message, so start the text over.
		if ev.Part.MessageID != lastID {
			lastID = ev.Part.MessageID
			text = ""
		}
		text += ev.Part.Text
	}
	return strings.TrimSpace(text)
}
