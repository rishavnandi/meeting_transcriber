package asr

import "testing"

func TestHasDevanagari(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"hindi", "हमने तय किया कि शुक्रवार को रिलीज़ होगा", true},
		{"english", "we decided to release on Friday", false},
		{"hinglish", "मैं ठीक हूँ, thanks for the update", true},
		{"empty", "", false},
		{"other script", " releasing  Friday  ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasDevanagari(tt.in); got != tt.want {
				t.Errorf("HasDevanagari(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSampleWindows(t *testing.T) {
	// Short enough to take whole.
	if w := sampleWindows(20); len(w) != 1 || w[0].length != 20 {
		t.Errorf("a 20 second file should be one window of 20 seconds, got %+v", w)
	}
	// Long enough for two windows.
	if w := sampleWindows(60); len(w) != 2 {
		t.Errorf("a one minute file should give two windows, got %d", len(w))
	}
	// Long enough to sample at the start, the middle and the end.
	w := sampleWindows(3600)
	if len(w) != 3 {
		t.Fatalf("an hour should give three windows, got %d", len(w))
	}
	if w[0].offset >= w[1].offset || w[1].offset >= w[2].offset {
		t.Errorf("windows should be in order, got %+v", w)
	}
	for _, win := range w {
		if win.offset < 0 || win.offset+win.length > 3600 {
			t.Errorf("window %+v falls outside the file", win)
		}
	}
}

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
