package summarize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, lines int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.txt")
	var b strings.Builder
	for range lines {
		b.WriteString("[00:00] speaker: line ")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSplitTranscriptShort(t *testing.T) {
	// A meeting of an hour or so fits in one part.
	path := writeTranscript(t, 900)
	chunks, err := splitTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Errorf("a 900 line transcript should be one chunk, got %d", len(chunks))
	}
}

func TestSplitTranscriptLong(t *testing.T) {
	// A long meeting is cut into parts, and no part may be big enough for the
	// model's read tool to stop short.
	path := writeTranscript(t, 5000)
	chunks, err := splitTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("a 5000 line transcript should be split, got %d chunks", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		lines := strings.Count(c, "\n") + 1
		if lines > chunkLines {
			t.Errorf("a chunk of %d lines is over the limit of %d", lines, chunkLines)
		}
		if len(c) > chunkBytes {
			t.Errorf("a chunk of %d bytes is over the limit of %d", len(c), chunkBytes)
		}
		total += lines
	}
	// Nothing may be lost.
	if total < 5000 {
		t.Errorf("the chunks hold %d lines, the transcript had 5000", total)
	}
}

func TestSplitTranscriptKeepsEveryLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.txt")
	content := "alpha\nbravo\ncharlie\ndelta\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	chunks, err := splitTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(chunks, "\n")
	for _, want := range []string{"alpha", "bravo", "charlie", "delta"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the chunks are missing %q", want)
		}
	}
}

func TestSplitTranscriptSplitsAVeryLongLine(t *testing.T) {
	// A line longer than the byte cap has to be cut, not dropped.
	path := filepath.Join(t.TempDir(), "transcript.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("y", chunkBytes+1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	chunks, err := splitTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("a very long line should be cut, got %d chunk(s)", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if total < chunkBytes {
		t.Errorf("the chunks hold %d bytes, the file had %d", total, chunkBytes+1000)
	}
}
