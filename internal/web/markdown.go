package web

import (
	"html/template"
	"regexp"
	"strings"
)

// The summary arrives as markdown written by a language model, and the app
// renders it as HTML.
//
// This is a renderer for the subset of markdown that shows up in a meeting
// note: headings, bullet lists, numbered lists, tables, block quotes and
// paragraphs, with bold, italic and inline code. It is not a full markdown
// implementation, and it does not need to be.
//
// The text is escaped before any markup is added, so a summary that contains
// angle brackets cannot inject HTML into the page.

var (
	reBold  = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reCode  = regexp.MustCompile("`([^`]+)`")
	reItal  = regexp.MustCompile(`(^|[\s(])\*([^*\n]+)\*`)
	reItalU = regexp.MustCompile(`(^|[\s(])_([^_\n]+)_`)
)

// renderMarkdown turns a markdown summary into HTML.
func renderMarkdown(src string) template.HTML {
	src = stripLeadingSummaryHeading(src)
	if strings.TrimSpace(src) == "" {
		return ""
	}
	lines := strings.Split(src, "\n")
	var b strings.Builder

	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])

		switch {
		case line == "":
			i++

		case strings.HasPrefix(line, "### "):
			b.WriteString("<h3>" + inline(line[4:]) + "</h3>")
			i++

		case strings.HasPrefix(line, "## "):
			b.WriteString("<h2>" + inline(line[3:]) + "</h2>")
			i++

		case strings.HasPrefix(line, "# "):
			b.WriteString("<h1>" + inline(line[2:]) + "</h1>")
			i++

		case strings.HasPrefix(line, ">"):
			var quote []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				quote = append(quote, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
				i++
			}
			b.WriteString("<blockquote>" + inline(strings.Join(quote, " ")) + "</blockquote>")

		case isTableRow(line) && i+1 < len(lines) && isTableDivider(lines[i+1]):
			// A table is a header row, a divider row, then the body rows.
			head := tableCells(lines[i])
			i += 2
			var body [][]string
			for i < len(lines) && isTableRow(lines[i]) {
				body = append(body, tableCells(lines[i]))
				i++
			}
			b.WriteString(renderTable(head, body))

		case isBullet(line):
			var items []string
			for i < len(lines) && isBullet(strings.TrimSpace(lines[i])) {
				items = append(items, strings.TrimSpace(lines[i])[2:])
				i++
			}
			b.WriteString("<ul>")
			for _, it := range items {
				b.WriteString("<li>" + inline(it) + "</li>")
			}
			b.WriteString("</ul>")

		case isOrdered(line):
			var items []string
			for i < len(lines) && isOrdered(strings.TrimSpace(lines[i])) {
				items = append(items, orderedText(strings.TrimSpace(lines[i])))
				i++
			}
			b.WriteString("<ol>")
			for _, it := range items {
				b.WriteString("<li>" + inline(it) + "</li>")
			}
			b.WriteString("</ol>")

		default:
			var para []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") ||
					isBullet(t) || isOrdered(t) || isTableRow(t) {
					break
				}
				para = append(para, t)
				i++
			}
			if len(para) > 0 {
				b.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>")
			}
		}
	}
	return template.HTML(b.String())
}

// stripLeadingSummaryHeading drops a "## Summary" heading from the top of a
// summary. The panel it goes in is already called Summary, so the heading
// would say the same thing twice.
//
// Only the very first heading is touched. If the summary starts with anything
// else it is left exactly as it is.
func stripLeadingSummaryHeading(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.EqualFold(t, "## Summary") {
			lines = lines[i+1:]
		}
		break
	}
	return strings.Join(lines, "\n")
}

// renderTable writes a markdown table.
func renderTable(head []string, body [][]string) string {
	var b strings.Builder
	b.WriteString("<table><thead><tr>")
	for _, c := range head {
		b.WriteString("<th>" + inline(c) + "</th>")
	}
	b.WriteString("</tr></thead><tbody>")
	for _, row := range body {
		b.WriteString("<tr>")
		for _, c := range row {
			b.WriteString("<td>" + inline(c) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")
	return b.String()
}

// inline escapes the text and then adds the inline markup.
func inline(s string) string {
	s = template.HTMLEscapeString(s)
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reCode.ReplaceAllString(s, "<code>$1</code>")
	s = reItal.ReplaceAllString(s, "$1<em>$2</em>")
	s = reItalU.ReplaceAllString(s, "$1<em>$2</em>")
	return s
}

// isBullet reports whether a line is a bullet item.
func isBullet(line string) bool {
	return strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")
}

// isOrdered reports whether a line is a numbered item.
func isOrdered(line string) bool {
	n, _, ok := strings.Cut(line, ". ")
	return ok && n != "" && strings.Trim(n, "0123456789") == ""
}

// orderedText strips the number from a numbered item.
func orderedText(line string) string {
	_, rest, _ := strings.Cut(line, ". ")
	return rest
}

// isTableRow reports whether a line looks like a table row.
func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|") && len(t) > 1
}

// isTableDivider reports whether a line is the dashes row under a table header.
//
// A divider cell is one or more dashes, with optional colons for alignment, so
// "---", "-----" and ":---:" all count.
func isTableDivider(line string) bool {
	t := strings.TrimSpace(line)
	if !isTableRow(t) {
		return false
	}
	cells := tableCells(t)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		dashes := strings.Trim(c, ":")
		if dashes == "" {
			return false
		}
		for _, r := range dashes {
			if r != '-' {
				return false
			}
		}
	}
	return true
}

// tableCells splits a table row into its cells.
func tableCells(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}
