package asr

import (
	"fmt"
	"strings"
)

// TranscriptText renders the recognised words as readable timestamped text.
//
// The recogniser gives word level timings but no paragraphs, so the text is
// grouped here. A new line starts when the speaker changes, when there is a
// pause, or when the line grows past a comfortable length, and each line is
// prefixed with the time it starts at.
//
// When the recording was diarised, the line also names the speaker. The
// recogniser only knows that two voices are different, not what anyone is
// called, so the name is a number.
//
// The result is what the summariser reads, so the timestamps help it refer to
// moments in the meeting.
func (r *Result) TranscriptText() string {
	var b strings.Builder
	var line []string
	lineStart := 0.0
	prevEnd := 0.0
	speaker := 0

	flush := func() {
		if len(line) == 0 {
			return
		}
		text := strings.Join(line, " ")
		if speaker > 0 {
			fmt.Fprintf(&b, "[%s] Speaker %d: %s\n", clock(lineStart), speaker, text)
		} else {
			fmt.Fprintf(&b, "[%s] %s\n", clock(lineStart), text)
		}
		line = line[:0]
	}

	for _, w := range r.Words {
		if w.Word == "" {
			continue
		}
		if len(line) == 0 {
			lineStart = w.Start
			speaker = w.Speaker
		} else {
			// A change of speaker starts a new line, as does a pause of more
			// than a second and a half, or a line that has grown too long.
			// The recogniser puts small gaps between words, so the pause
			// threshold is generous on purpose.
			if w.Speaker != speaker || w.Start-prevEnd > 1.5 || lineLen(line) > 110 {
				flush()
				lineStart = w.Start
				speaker = w.Speaker
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
