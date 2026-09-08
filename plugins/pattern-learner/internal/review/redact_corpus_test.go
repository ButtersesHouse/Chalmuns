package review

import (
	"encoding/json"
	"strings"
	"testing"
)

// The redaction corpus. It is pinned in both directions on purpose: this
// package has rewritten these patterns several times, and each rewrite that
// widened them started rewriting the reviewer's own words, while each one that
// narrowed them started letting a credential through. Both are load-bearing —
// the artifact is committed to the repository, and it is also the text
// verify-grounding matches a signal's quote against — so a change to
// redactSecrets is only correct if every row here still holds.

// mustRedact: the credential must not survive, and the named context must.
var redactCorpus = []struct{ name, in, absent, keep string }{
	{"shell assignment", `SEMGREP_APP_TOKEN=sk-secret-abc123 semgrep --json .`, "sk-secret-abc123", "semgrep --json ."},
	{"auth assignment", `auth=deadbeefcafe1234 curl x`, "deadbeefcafe1234", "curl x"},
	{"basic auth assignment", `BASIC_AUTH=user:supersecretvalue`, "supersecretvalue", "BASIC_AUTH"},
	{"json field", `{"env":{"SEMGREP_APP_TOKEN":"sk-secret-abc123"}}`, "sk-secret-abc123", "SEMGREP_APP_TOKEN"},
	{"json field with a space", `{"api_key": "abc def ghi"}`, "def", "api_key"},
	{"single-quoted python dict", `{'password': 'hunter2trustno1'}`, "hunter2trustno1", "password"},
	{"single-quoted shell value", `export MY_PASSWORD='p@ss w0rd'`, "w0rd", "MY_PASSWORD"},
	{"yaml colon form", "database:\n  password: hunter2trustno1\n", "hunter2trustno1", "database:"},
	{"env dump colon form", `AWS_SECRET_ACCESS_KEY: wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY`, "wJalrXUtnFEMI", "AWS_SECRET_ACCESS_KEY"},
	{"header dump colon form", `X-Api-Key: abcdefghijklmnop`, "abcdefghijklmnop", "X-Api-Key"},
	{"flag form", `semgrep --token sk-secret-abc123 .`, "sk-secret-abc123", "semgrep"},
	{"argv entry", `{"argv":["semgrep","--token","sk-secret-abc123"]}`, "sk-secret-abc123", "semgrep"},
	{"json authorization", `{"headers":{"Authorization":"Bearer eyJhbGciOi.SUPERSECRET.sig"}}`, "SUPERSECRET", "Authorization"},
	{"json basic authorization", `{"Authorization": "Basic dXNlcjpzdXBlcnNlY3JldA=="}`, "dXNlcjpzdXBlcnNlY3JldA", "Authorization"},
	{"curl header", `curl -H "Authorization: Bearer eyJhbGciOi.abc.def" https://x`, "eyJhbGciOi.abc.def", "https://x"},
	{"github token", `ran with ghp_abcdefghijklmnopqrstuvwxyz0123456789`, "ghp_abcdef", "ran with"},
	{"aws key id", `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE aws s3 ls`, "AKIAIOSFODNN7EXAMPLE", "aws s3 ls"},
	{"slack token", `posted with xoxb-1234567890-abcdefghij`, "xoxb-1234567890", "posted with"},
	{"pem block", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAy8Dbv8prpJ\nAAAA\n-----END RSA PRIVATE KEY-----", "MIIEowIBAAKCAQEA", ""},
	{"truncated json", `{"results":[{"m":"ok"}],"command":"SEMGREP_APP_TOKEN=sk-secret-abc123 semgrep`, "sk-secret-abc123", `"m":"ok"`},
	{"nested log string", `{"results":[{"m":"ok"}],"log":"{\"command\":\"TOKEN=sk-secret-abc123 semgrep\"}"}`, "sk-secret-abc123", `"m":"ok"`},
	// Rows added because the ones above them passed for the wrong reason: a
	// fake value carrying the word "secret", or a `sk-` prefix reBareSecret
	// catches on its own. Each of these has a value nothing else would match.
	{"auth name segment", `X_AUTH_HDR=8f3a9b2c1d`, "8f3a9b2c1d", "X_AUTH_HDR"},
	{"oauth prefix", `OAUTH=abcdefghij`, "abcdefghij", "OAUTH"},
	{"base64 basic auth", `BASIC_AUTH=dXNlcjpwYXNzd29yZA==`, "dXNlcjpwYXNzd29yZA", "BASIC_AUTH"},
	{"a source line echoed inside JSON", `{"extra":{"lines":"password = \"hunter2trustno1\""}}`, "hunter2trustno1", "lines"},
	{"a short alphabetic password", `password: swordfish`, "swordfish", "password"},
	{"a plain word password", `password: correcthorse`, "correcthorse", "password"},
	{"a truncated pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAy8Dbv8prpJ\nAAAA", "MIIEowIBAAKCAQEA", ""},
	{"a flag with an equals sign", `deploy --token=abcdef123456 --force`, "abcdef123456", "--force"},
}

// mustNotRedact: the reviewer's own words, untouched. Every one of these was
// rewritten by some version of these patterns.
var proseCorpus = []struct{ name, in string }{
	{"a colon in prose", "- The credential: check is missing in handler.go"},
	{"a quoted word after a colon in prose", `- The credential: "auth.go" is missing from "main.go"`},
	{"authorization in prose", "- Authorization: the middleware is skipped for /health"},
	{"a quote two lines below a secret-ish word", "Findings about the api_key:\n\n\"Use env vars\" — note"},
	{"a hyphenated path", "internal/task-scheduler/run.go and risk-assessment and flask-app"},
	{"angle brackets and ampersands", "prefer a < b && c > d, and pass --json to it."},
	{"a large number", `{"line":9007199254740993}`},
	{"a fenced config example", "```json\n{\n  \"args\": [\"--json\"],\n  \"rule\": \"x\"\n}\n```"},
	{"a short value after a colon", "token: yes"},
	{"a sentence about passwords", "The password policy is documented in docs/auth.md and applies to every service."},
	// Prose rows added for the same reason: each names a file the reviewer
	// cited, which a length-only rule rewrote into one that does not exist.
	{"a path after a secret-ish word", "- The api_key: internal/auth/token.go is unused"},
	{"a path with a hyphen", "the secret: docs/setup-guide.md explains it"},
	{"an identifier containing auth", "The token check is wrong: user.IsAuthenticated=true is never set."},
	{"an authorized field", "It sets authorized=true before validating the token."},
}

// mustStayValid: redaction may not make a document unparseable. A watcher
// pinned to a format gets an error from Capture and loses the whole review;
// an auto one degrades to prose and loses every finding.
var jsonCorpus = []string{
	`{"results":[{"check_id":"c"}],"auth":{"required":true}}`,
	`{"results":[],"auth_required":false}`,
	`{"results":[],"token_count":1234}`,
	`{"results":[],"secrets":["a","b"]}`,
	`{"env":{"SEMGREP_APP_TOKEN":"sk-secret-abc123"}}`,
	`{"api_key":"abc def"}`,
	`{"headers":{"Authorization":"Bearer eyJ.abc.def"}}`,
	`{"results":[{"check_id":"c","extra":{"metadata":{"secret":"matches the \"AKIA\" prefix"}}},{"check_id":"d"}]}`,
	`{"results":[{"extra":{"lines":"-----BEGIN RSA PRIVATE KEY-----"}}],"version":"1.55.2"}`,
	// A value whose end is an escaped quote: a class that ate the backslash
	// left a bare quote behind and made the document unparseable, which cost a
	// pinned-format watcher the whole review.
	`{"results":[{"extra":{"message":"the token: internal/auth/x.go\" quoted","severity":"ERROR"}}]}`,
	`{"m":"secret: a/b/c/d/e/f\"g"}`,
}

func TestRedactSecrets_removesCredentials(t *testing.T) {
	for _, tc := range redactCorpus {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if strings.Contains(got, tc.absent) {
				t.Errorf("%q survived redaction: %q", tc.absent, got)
			}
			if tc.keep != "" && !strings.Contains(got, tc.keep) {
				t.Errorf("%q was lost: %q", tc.keep, got)
			}
		})
	}
}

func TestRedactSecrets_leavesTheReviewAlone(t *testing.T) {
	for _, tc := range proseCorpus {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSecrets(tc.in); got != tc.in {
				t.Errorf("the reviewer's own words were rewritten:\n  in:  %q\n  out: %q", tc.in, got)
			}
		})
	}
}

func TestRedactSecrets_keepsDocumentsParseable(t *testing.T) {
	for _, in := range jsonCorpus {
		got := redactSecrets(in)
		if !json.Valid([]byte(got)) {
			t.Errorf("redaction broke JSON:\n  in:  %s\n  out: %s", in, got)
		}
	}
}
