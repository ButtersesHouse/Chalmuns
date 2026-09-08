package review

import (
	"strings"
	"testing"
)

// Credentials are removed from the text itself, in place, leaving every other
// byte as the reviewer wrote it. A structural scrub of the decoded document —
// delete the fields that hold an invocation — had to be right about the shape
// of every payload it would ever see, and never was: a truncated document was
// skipped whole, an invocation log carried as a string field was invisible,
// and re-encoding what it kept reordered keys, escaped angle brackets and
// rewrote large integers in text this package documents as byte-for-byte.
func TestRedactSecrets(t *testing.T) {
	cases := []struct{ name, in, absent, keep string }{
		{"env assignment", `{"command":"SEMGREP_APP_TOKEN=sk-secret-abc123 semgrep --json ."}`, "sk-secret-abc123", "semgrep --json ."},
		{"json field", `{"env":{"SEMGREP_APP_TOKEN":"sk-secret-abc123"}}`, "sk-secret-abc123", "SEMGREP_APP_TOKEN"},
		{"argv entry", `{"argv":["semgrep","--token","sk-secret-abc123"]}`, "sk-secret-abc123", "semgrep"},
		{"truncated json", `{"results":[{"m":"ok"}],"command":"SEMGREP_APP_TOKEN=sk-secret-abc123 semgrep`, "sk-secret-abc123", `"m":"ok"`},
		{"nested log string", `{"results":[{"m":"ok"}],"log":"{\"command\":\"TOKEN=sk-secret-abc123 semgrep\"}"}`, "sk-secret-abc123", `"m":"ok"`},
		{"github token", `ran with ghp_abcdefghijklmnopqrstuvwxyz0123456789`, "ghp_abcdef", "ran with"},
		{"aws key", `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE aws s3 ls`, "AKIAIOSFODNN7EXAMPLE", "aws s3 ls"},
		{"authorization header", `curl -H "Authorization: Bearer eyJhbGciOi.abc.def" https://x`, "eyJhbGciOi.abc.def", "https://x"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----", "BEGIN RSA PRIVATE KEY", ""},
		{"prose is untouched", "## Review\n\nprefer a < b && c > d, and pass --json to it.", "", "prefer a < b && c > d, and pass --json to it."},
		{"a big number is untouched", `{"line":9007199254740993}`, "", "9007199254740993"},
		{"a quoted config in prose is untouched", "```json\n{\n  \"args\": [\"--json\"],\n  \"rule\": \"x\"\n}\n```", "", "\"args\": [\"--json\"]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Errorf("%q survived: %q", tc.absent, got)
			}
			if tc.keep != "" && !strings.Contains(got, tc.keep) {
				t.Errorf("%q was lost: %q", tc.keep, got)
			}
		})
	}
}
