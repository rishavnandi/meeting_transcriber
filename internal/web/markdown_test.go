package web

import "testing"

func TestRenderMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "headings",
			in:   "## Decisions\n\nSome text.",
			want: "<h2>Decisions</h2><p>Some text.</p>",
		},
		{
			name: "leading summary heading is dropped",
			in:   "## Summary\n\nSome text.",
			want: "<p>Some text.</p>",
		},
		{
			name: "bullets",
			in:   "- one\n- two",
			want: "<ul><li>one</li><li>two</li></ul>",
		},
		{
			name: "ordered",
			in:   "1. first\n2. second",
			want: "<ol><li>first</li><li>second</li></ol>",
		},
		{
			name: "bold and code",
			in:   "Use **deploy** now, see `README`.",
			want: "<p>Use <strong>deploy</strong> now, see <code>README</code>.</p>",
		},
		{
			name: "table",
			in:   "| Owner | Action |\n| --- | --- |\n| Rahul | Ship |",
			want: "<table><thead><tr><th>Owner</th><th>Action</th></tr></thead><tbody><tr><td>Rahul</td><td>Ship</td></tr></tbody></table>",
		},
		{
			name: "escapes html",
			in:   "a <script>alert(1)</script> b",
			want: "<p>a &lt;script&gt;alert(1)&lt;/script&gt; b</p>",
		},
		{
			name: "empty",
			in:   "   \n  ",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(renderMarkdown(tt.in))
			if got != tt.want {
				t.Errorf("renderMarkdown(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRenderMarkdownDoesNotBreakOutOfEmphasis(t *testing.T) {
	// A summary is model output, so it can contain anything.
	in := "**bold** then *italic* and _also_ and `code`"
	got := string(renderMarkdown(in))
	for _, want := range []string{"<strong>bold</strong>", "<em>italic</em>", "<em>also</em>", "<code>code</code>"} {
		if !contains(got, want) {
			t.Errorf("output %q is missing %q", got, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
