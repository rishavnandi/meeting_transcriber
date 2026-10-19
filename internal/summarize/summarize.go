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
	"os/exec"
	"strings"
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

// event is one line of OpenCode's JSON output.
type event struct {
	Type string `json:"type"`
	Part struct {
		MessageID string `json:"messageID"`
		Text      string `json:"text"`
	} `json:"part"`
}

// Summarize reads a transcript file and returns the summary as markdown.
func (s *Summarizer) Summarize(ctx context.Context, transcriptPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	// The message goes before --file. --file takes a list of values, so a
	// message placed after it would be swallowed as another filename.
	cmd := exec.CommandContext(ctx, s.cfg.Binary,
		"run", "--model", s.cfg.Model, "--format", "json",
		prompt, "--file", transcriptPath)
	cmd.Dir = transcriptPath[:strings.LastIndex(transcriptPath, "/")]

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
