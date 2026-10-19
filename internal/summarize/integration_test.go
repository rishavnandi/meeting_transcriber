package summarize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSummarizeLongTranscript runs the whole path against a real model.
//
// It is skipped unless SUMMARIZE_TEST is set, because it needs opencode and a
// model to talk to. The point of it is the thing that cannot be unit tested:
// that a meeting longer than the model's read tool can return still gets a
// summary covering its end.
//
//	SUMMARIZE_TEST=1 go test ./internal/summarize/ -run Long
func TestSummarizeLongTranscript(t *testing.T) {
	if os.Getenv("SUMMARIZE_TEST") == "" {
		t.Skip("set SUMMARIZE_TEST=1 to run this against a real model")
	}

	// The last line carries a detail that only appears there. If the summary
	// mentions it, the whole transcript was read.
	const secret = "Blue Heron"

	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.txt")
	var b strings.Builder
	for i := range 3000 {
		if i == 2999 {
			fmt.Fprintf(&b, "[50:00] Priya: The final decision was to name the project %s.\n", secret)
			continue
		}
		fmt.Fprintf(&b, "[%02d:%02d] speaker%d: we discussed the roadmap and the vendor contract number %d\n", i/60%60, i%60, i%3, i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(Config{
		Binary:  envOr("OPENCODE_BIN", "opencode"),
		Model:   envOr("SUMMARY_MODEL", "opencode/big-pickle"),
		Timeout: 15 * time.Minute,
	}, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	out, err := s.Summarize(ctx, path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("summary:\n%s", out)
	if !strings.Contains(out, secret) {
		t.Errorf("the summary does not mention %q, so the end of the meeting was not read", secret)
	}
}

// envOr reads an environment variable with a fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
