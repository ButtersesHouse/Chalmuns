package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactSecrets_shapes(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAy8Dbv8prpJ/0kKhlGeJYozo2t60EG8L0561g13R29LvMR5hy\nAAAA\n-----END RSA PRIVATE KEY-----"
	cases := []struct{ name, in, absent, keep string }{
		{"pem body", pem, "MIIEowIBAAKCAQEA", ""},
		{"json authorization", `{"headers":{"Authorization":"Bearer eyJhbGciOi.SUPERSECRET.sig"}}`, "SUPERSECRET", "Authorization"},
		{"json basic", `{"Authorization": "Basic dXNlcjpzdXBlcnNlY3JldA=="}`, "dXNlcjpzdXBlcnNlY3JldA", "Authorization"},
		{"header authorization", `curl -H "Authorization: Bearer eyJhbGciOi.abc.def" https://x`, "eyJhbGciOi.abc.def", "https://x"},
		{"quoted value with a space", `export MY_PASSWORD='p@ss w0rd'`, "w0rd", "MY_PASSWORD"},
		{"json quoted value with a space", `{"api_key": "abc def ghi"}`, "def", "api_key"},
		{"prose colon is untouched", "- The credential: check is missing in handler.go", "[redacted]", "The credential: check is missing"},
		{"prose authorization is untouched", "- Authorization: the middleware is skipped for /health", "[redacted]", "the middleware is skipped"},
		{"hyphenated word untouched", "internal/task-scheduler/run.go and risk-assessment and flask-app", "[redacted]", "task-scheduler"},
		{"env assignment", `SEMGREP_APP_TOKEN=sk-secret-abc123 semgrep --json .`, "sk-secret-abc123", "semgrep --json ."},
		{"json field", `{"env":{"SEMGREP_APP_TOKEN":"sk-secret-abc123"}}`, "sk-secret-abc123", "SEMGREP_APP_TOKEN"},
		{"argv entry", `{"argv":["semgrep","--token","sk-secret-abc123"]}`, "sk-secret-abc123", "semgrep"},
		{"truncated json", `{"results":[{"m":"ok"}],"command":"SEMGREP_APP_TOKEN=sk-secret-abc123 semgrep`, "sk-secret-abc123", `"m":"ok"`},
		{"github token", `ran with ghp_abcdefghijklmnopqrstuvwxyz0123456789`, "ghp_abcdef", "ran with"},
		{"aws key", `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE aws s3 ls`, "AKIAIOSFODNN7EXAMPLE", "aws s3 ls"},
		{"prose untouched", "## Review\n\nprefer a < b && c > d, and pass --json to it.", "[redacted]", "prefer a < b && c > d, and pass --json to it."},
		{"big number untouched", `{"line":9007199254740993}`, "[redacted]", "9007199254740993"},
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

// Redacting must leave the document parseable: a watcher pinned to a format
// gets an error from Capture and the whole review is dropped, and an auto one
// degrades to prose and loses its findings. A rule that fired on a container
// or a number under an auth-ish name did exactly that.
func TestRedactSecrets_keepsJSONValid(t *testing.T) {
	docs := []string{
		`{"results":[{"check_id":"c"}],"auth":{"required":true}}`,
		`{"results":[],"auth_required":false}`,
		`{"results":[],"token_count":1234}`,
		`{"results":[],"secrets":["a","b"]}`,
		`{"env":{"SEMGREP_APP_TOKEN":"sk-secret-abc123"}}`,
		`{"api_key":"abc def"}`,
		`{"headers":{"Authorization":"Bearer eyJ.abc.def"}}`,
	}
	for _, d := range docs {
		got := redactSecrets(d)
		if !json.Valid([]byte(got)) {
			t.Errorf("redaction broke JSON:\n  in:  %s\n  out: %s", d, got)
		}
	}
}

// The runner's own fields are dropped at every level of a decoded response,
// and a response that is nothing but those fields records nothing at all.
func TestFromHook_theRunnersOwnAccountIsNotAReview(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	cases := []struct {
		name, response string
		wantOK         bool
		absent, keep   string
	}{
		{"nested stderr is dropped", `{"results":[{"check_id":"c","path":"a.py","extra":{"message":"Use the shared client."}}],"metadata":{"stderr":"Traceback (most recent call last):\n  File x, line 3\nRuntimeError: boom"}}`, true, "Traceback", "check_id"},
		{"a decoded report under an input key", `{"input":{"findings":[{"file":"a.go","summary":"Use the shared client everywhere."}]}}`, true, "", "shared client"},
		{"a json authorization in a nested field", `{"results":[{"check_id":"c","path":"a.py","extra":{"message":"Use the shared client."}}],"run":{"headers":{"Authorization":"Bearer eyJ.SUPERSECRET.sig"}}}`, true, "SUPERSECRET", "check_id"},
		{"a bookkeeping-only payload", `{"stdout":"{\"cwd\":\"/home/me/private-client-project\",\"exit_code\":0}"}`, false, "", ""},
		{"a foreign findings report survives", `{"findings":[{"id":"SNYK-JS-1","title":"Prototype pollution","severity":"high"}]}`, true, "", "SNYK-JS-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},"tool_response":` + tc.response + `}`
			a, _, ok := FromHook([]byte(payload), ws, fixed)
			if ok != tc.wantOK {
				t.Fatalf("ok: want %v got %v (raw %q)", tc.wantOK, ok, a.RawText)
			}
			if tc.absent != "" && strings.Contains(a.RawText, tc.absent) {
				t.Errorf("%q survived: %q", tc.absent, a.RawText)
			}
			if tc.keep != "" && !strings.Contains(a.RawText, tc.keep) {
				t.Errorf("%q was lost: %q", tc.keep, a.RawText)
			}
		})
	}
}
