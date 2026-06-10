package spawn

import (
	"testing"
)

// TestTrimOutput verifies trimOutput correctly finds the first line whose first
// non-space character is '{'.
func TestTrimOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON",
			input: `{"type":"result","result":"ok"}` + "\n",
			want:  `{"type":"result","result":"ok"}` + "\n",
		},
		{
			name:  "noisy prefix line before JSON",
			input: "some log line\n" + `{"type":"result","result":"ok"}` + "\n",
			want:  `{"type":"result","result":"ok"}` + "\n",
		},
		{
			name: "noisy prefix line containing { mid-line",
			// The critical bug fix: a line like "building {thing}" should NOT be
			// treated as the JSON line even though it contains '{'.
			input: "building {thing} at 12:00\n" + `{"type":"result","result":"ok"}` + "\n",
			want:  `{"type":"result","result":"ok"}` + "\n",
		},
		{
			name:  "leading whitespace before {",
			input: "  \t  \n" + `{"type":"result"}` + "\n",
			want:  `{"type":"result"}` + "\n",
		},
		{
			name:  "multiple noisy lines then JSON",
			input: "line 1\nline 2 has {curly}\nline3\n" + `{"type":"result","cost_usd":0.01}` + "\n",
			want:  `{"type":"result","cost_usd":0.01}` + "\n",
		},
		{
			name:  "no JSON line — returns full buffer",
			input: "no json here\njust plain text\n",
			want:  "no json here\njust plain text\n",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "JSON without trailing newline",
			input: `{"type":"result"}`,
			want:  `{"type":"result"}`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := string(trimOutput([]byte(tc.input)))
			if got != tc.want {
				t.Errorf("trimOutput(%q)\n  got:  %q\n  want: %q", tc.input, got, tc.want)
			}
		})
	}
}
