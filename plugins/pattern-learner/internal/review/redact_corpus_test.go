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
	// Rows for the leaks the eighteenth review found. Each value is shaped so
	// nothing else in the pattern set would catch it.
	{"a base64 secret with slashes", `aws_secret_access_key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`, "wJalrXUtnFEMI", "aws_secret_access_key"},
	{"a base64 value with a slash", `secret: dXNlcjpwYXNz/d29yZA==`, "dXNlcjpwYXNz", "secret"},
	{"a single-quoted colon value", `password: 'hunter2trustno1'`, "hunter2trustno1", "password"},
	{"a single-quoted api key", `api_key: 'abcdefghijklmnop'`, "abcdefghijklmnop", "api_key"},
	{"a jwt", `token: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc`, "eyJhbGciOiJIUzI1NiJ9", "token"},
	// A report that quotes a key's header line mid-sentence loses the marker
	// and nothing else: a body class of letters-and-spaces swallowed the rest
	// of that finding and the one after it.
	{"a key header quoted mid-sentence", "## Findings\n\n- `config/prod.yaml` embeds -----BEGIN RSA PRIVATE KEY----- and commits it\n\n- The retry loop never backs off\n", "BEGIN RSA PRIVATE KEY", "The retry loop never backs off"},
	// Rows for the leaks the nineteenth review found.
	{"a colon with no space", `password:hunter2trustno1`, "hunter2trustno1", "password"},
	{"an indented pem", "private_key: |\n  -----BEGIN RSA PRIVATE KEY-----\n  MIIEowIBAAKCAQEAy8Dbv8prpJ\n  -----END RSA PRIVATE KEY-----\n", "MIIEowIBAAKCAQEA", "private_key"},
	{"a pem inside a JSON string", `{"path":"deploy/key.pem","lines":"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAy8Dbv8prpJ\n-----END RSA PRIVATE KEY-----"}`, "MIIEowIBAAKCAQEA", "deploy/key.pem"},
	// A credential is judged by what encloses it, not by its own shape, so
	// these no longer depend on the value looking a particular way.
	{"a quoted value with a space", `password: "p@ss w0rd"`, "p@ss w0rd", "password"},
	{"a single-quoted value with a space", `secret: 'p@ss w0rd'`, "p@ss w0rd", "secret"},
	{"a slashed value ending in an extension", `aws_secret_access_key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLE.key`, "wJalrXUtnFEMI", "aws_secret_access_key"},
	{"a config line echoed inside JSON", `{"results":[{"extra":{"lines":"  aws_secret_access_key: wJalrXUtnFEMI/K7MDENG"}}]}`, "wJalrXUtnFEMI", "results"},
	{"a config line echoed beside a message", `{"results":[{"extra":{"lines":"password: hunter2trustno1"},"m":"Do not commit"}]}`, "hunter2trustno1", "Do not commit"},
	{"a yaml list item", "- password: hunter2trustno1", "hunter2trustno1", "password"},
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
	// Rows for the over-redaction the same review found: a cited path longer
	// than any the corpus held, and a quoted sentence after a secret-ish word.
	// The corpus had grown only in the redact direction, which is what let a
	// length shortcut past the file-reference guard go unnoticed.
	{"a long cited path", "- The api_key: internal/auth/token_provider.go is unused"},
	{"a long cited config path", "- The token: config/production/settings.yaml holds it"},
	{"a long quoted cited path", `- The credential: "internal/authentication_helper.go" is missing`},
	{"a quoted requirement", `### 2. Hardcoded password: "must be at least 12 characters"`},
	{"a quoted instruction", `The secret: "do not commit this" appears in the README`},
	{"a path with an underscore", "The token: internal/auth/session_token_store.go never expires."},
	// One unbalanced quote after a secret-ish name deleted every finding up to
	// the next quote in the review.
	{"an unbalanced quote after a name", "## Findings\n\n### 1. Hardcoded password: \"admin — see the note\n\nThe handler compares the value directly.\n\n### 2. Missing rate limit\n\nUse a \"token bucket\" here.\n\n### 3. Retry loop never backs off\n"},
	// Code the reviewer quoted, and a value with words after it: an
	// assignment's line ends at the value, a sentence's does not.
	{"a struct field in prose", "- The struct sets Token:tokenValue without validation"},
	{"a section name after a colon", "Note the password:overview section"},
	{"a code fragment with prose after it", "apiKey:process.env.API_KEY is read at startup"},
	{"a short value", "token: yes"},
	// A colon at the end of a line, and a report that quotes a key's header
	// line mid-sentence: both had every finding after them deleted.
	{"a heading ending in a colon", "## Findings\n\n### 1. Hardcoded credential:\n\nThe handler compares the value directly.\n\n### 2. Missing rate limit\n\nThe endpoint is unbounded.\n"},
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
	// A JSON escape after a secret-ish word: splitting it left `\[redacted]`
	// and an unparseable document, which cost a pinned-format watcher the
	// whole review.
	`{"m":"the secret:\nhunter2trustno1 is hardcoded"}`,
	`{"m":"the token:\thunter2trustno1 is hardcoded"}`,
	// A credential in the middle of a nested string, with report text after
	// it: a value class that ate the escaped quote deleted everything between.
	`{"results":[{"extra":{"lines":"{\"api_key\": \"abc123def\", \"note\": \"this is the finding text\"}"}}],"version":"1.55"}`,
	`{"results":[{"extra":{"lines":"  password: hunter2trustno1"},"m":"x"}]}`,
	`{"api_key":"abc def"}`,
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
