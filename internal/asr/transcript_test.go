package asr

import "testing"

func TestTranscriptText(t *testing.T) {
	res := &Result{
		Text: "one two three four",
		Words: []Word{
			{Word: "one", Start: 0, End: 0.4},
			{Word: "two", Start: 0.5, End: 0.9},
			// A gap of more than a second and a half starts a new line.
			{Word: "three", Start: 3.0, End: 3.5},
			{Word: "four", Start: 3.6, End: 4.0},
		},
	}
	got := res.TranscriptText()
	want := "[00:00] one two\n[00:03] three four\n"
	if got != want {
		t.Errorf("TranscriptText()\n got %q\nwant %q", got, want)
	}
}

func TestTranscriptTextKeepsShortGapsTogether(t *testing.T) {
	res := &Result{
		Words: []Word{
			{Word: "one", Start: 0, End: 0.4},
			{Word: "two", Start: 1.4, End: 1.8},
		},
	}
	if got := res.TranscriptText(); got != "[00:00] one two\n" {
		t.Errorf("TranscriptText() = %q, want both words on one line", got)
	}
}

func TestTranscriptTextFallsBackToPlainText(t *testing.T) {
	res := &Result{Text: "no word timings here"}
	if got := res.TranscriptText(); got != "no word timings here" {
		t.Errorf("TranscriptText() = %q, want the plain text", got)
	}
}
