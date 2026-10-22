package asr

import (
	"fmt"
	"strings"
)

// TranscriptText renders the recognised words as readable timestamped text.
//
// The recogniser gives word level timings but no paragraphs, so the text is
// grouped here. A new line starts when there is a pause or when the line grows
// past a comfortable length, and each line is prefixed with the time it starts
// at.
//
// The result is what the summariser reads, so the timestamps help it refer to
// moments in the meeting.
func (r *Result) TranscriptText() string {
	var b strings.Builder
	var line []string
	lineStart := 0.0
	prevEnd := 0.0

	flush := func() {
		if len(line) == 0 {
			return
		}
		fmt.Fprintf(&b, "[%s] %s\n", clock(lineStart), strings.Join(line, " "))
		line = line[:0]
	}

	for _, w := range r.Words {
		if w.Word == "" {
			continue
		}
		if len(line) == 0 {
			lineStart = w.Start
		} else {
			// A pause of more than a second and a half starts a new line, as
			// does a line that has grown too long. The recogniser puts small
			// gaps between words, so the pause threshold is generous on
			// purpose.
			if w.Start-prevEnd > 1.5 || lineLen(line) > 110 {
				flush()
				lineStart = w.Start
			}
		}
		line = append(line, w.Word)
		prevEnd = w.End
	}
	flush()

	// The recogniser also returns a flat transcript. Keep it when there are no
	// word timings, so the app still shows something useful.
	if b.Len() == 0 {
		return strings.TrimSpace(r.Text)
	}
	return b.String()
}

// lineLen is the length of the words already collected.
func lineLen(line []string) int {
	n := 0
	for _, w := range line {
		n += len(w) + 1
	}
	return n
}

// clock formats seconds as mm:ss, or h:mm:ss past an hour.
func clock(seconds float64) string {
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
