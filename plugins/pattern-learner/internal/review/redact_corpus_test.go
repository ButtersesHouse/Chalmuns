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
	{"a slashed value", `aws_secret_access_key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`, "wJalrXUtnFEMI", "aws_secret_access_key"},
	{"a config line echoed inside JSON", `{"results":[{"extra":{"lines":"  aws_secret_access_key: wJalrXUtnFEMI/K7MDENG"}}]}`, "wJalrXUtnFEMI", "results"},
	{"a config line echoed beside a message", `{"results":[{"extra":{"lines":"password: hunter2trustno1"},"m":"Do not commit"}]}`, "hunter2trustno1", "Do not commit"},
	{"a yaml list item", "- password: hunter2trustno1", "hunter2trustno1", "password"},
	// The four shapes the surroundings-only rule leaked: something after the
	// value, a word before the name, a heading marker, and a second
	// credential on the same line.
	{"a trailing comment", `password: hunter2trustno1  # rotate this`, "hunter2trustno1", "rotate this"},
	{"a trailing line reference", `password: hunter2trustno1 (line 4)`, "hunter2trustno1", "(line 4)"},
	{"a trailing newline escape", `{"extra":{"lines":"password: hunter2trustno1\n"}}`, "hunter2trustno1", "lines"},
	{"a word before the name", `Using api_key: abcdef123456`, "abcdef123456", "Using api_key"},
	{"a heading before the name", `### 1. Hardcoded password: "hunter2trustno1"`, "hunter2trustno1", "Hardcoded password"},
	{"two credentials on one line", `password: hunter2trustno1, token: swordfish123`, "swordfish123", "token"},
	// A bullet is a bullet whichever character the reviewer used, and a code
	// span holding nothing but an assignment is an assignment.
	{"a plus bullet", `+ password: "p@ss w0rd"`, "p@ss w0rd", "password"},
	{"an ordered list item", `1. password: "p@ss w0rd"`, "p@ss w0rd", "password"},
	// A markdown delimiter is not part of the value; swallowing the closing
	// one rendered the rest of the write-up as code.
	{"a value in a code span", "`api_key: abcdefghijkl`", "abcdefghijkl", "api_key"},
	{"a value in bold", `**secret: hunter2trustno1**`, "hunter2trustno1", "secret"},
	// Rows for the leaks the twenty-fourth review found. A reviewer quoting an
	// assignment and then describing it is the commonest write-up shape there
	// is, and the span rule that stopped `token: sessionToken` being rewritten
	// let every one of these through.
	{"a bold value a sentence continues past", `- **DB_PASSWORD: hunter2trustno1** is committed in docker-compose.yml`, "hunter2trustno1", "docker-compose.yml"},
	{"a code-span value a sentence continues past", "- `api_key: abcdef123456` is hardcoded in config.go", "abcdef123456", "config.go"},
	{"an emphasised value a sentence continues past", `- _secret: hunter2trustno1_ appears twice`, "hunter2trustno1", "appears twice"},
	// An array under a secret-ish name holds credentials; dropping the key to
	// spare a list of rule names committed them.
	{"an array of credentials", `{"leaked_credentials":{"passwords":["a8f3d9e2c1b47f60"]}}`, "a8f3d9e2c1b47f60", "leaked_credentials"},
	{"a nested array of credentials", `{"secrets":[["a8f3d9e2c1b47f60"]]}`, "a8f3d9e2c1b47f60", "secrets"},
	// The ceiling on isFileReference: past it no path a review cites is that
	// long, so a separator and an extension stop excusing the value.
	{"a long slashed value with a dotted tail", `secret: wJalrXUtnFEMI/K7MDENGbPxRfiCYEXAMPLEKEY/AKIAIOSFODNN7EXAMPLEwJalrXUtn.key`, "wJalrXUtnFEMI", "secret"},
	// Rows for the leaks the twenty-sixth review found. "Looks like camelCase"
	// excused more than 999 random base64 strings in 1000, so every one of
	// these came back verbatim. A name has short pieces with separators between
	// them; sixteen unbroken characters is a payload.
	{"a capitalised array credential", `{"passwords":["aB8f3d9e2c1b47f60"]}`, "aB8f3d9e2c1b47f60", "passwords"},
	{"a slashed array credential", `{"passwords":["wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"]}`, "wJalrXUtnFEMI", "passwords"},
	{"a jwt in an array", `{"tokens":["eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc"]}`, "eyJhbGciOiJIUzI1NiJ9", "tokens"},
	// A line-start assignment is the strongest signal there is, so the name test
	// does not apply there: a credential chunked to look like a qualified name
	// is still redacted when the syntax says it is one. The single exception is
	// an indirection — `password: $DB_PASSWORD` — which the proseCorpus pins.
	{"a chunked credential in an assignment", `secret: aB3xY9zQ7wE.rT5yU8iO2p.aS4dF7gH1j`, "aB3xY9zQ7wE", "secret"},
	{"a quoted passphrase with a dot", `password: "p@ss w0rd.1"`, "p@ss w0rd.1", "password"},
	{"a capitalised constant-shaped credential", `AWS_SECRET_ACCESS_KEY: WJALRXUTNFEMI_K7MDENGBPXRFICYEXAMPLEKEY`, "WJALRXUTNFEMI", "AWS_SECRET_ACCESS_KEY"},
	{"a hex credential with an underscore", `api_key: 0F8A2B_C4D6E8FA0B2C4D6E8FA0B2C4D6E8FA0B2C`, "C4D6E8FA0B2C", "api_key"},
	{"a capitals-run path segment", `private_key: keys/WJALRXUTNFEMIKMDENGBPXRFICYEXAMPLEKEY.key`, "WJALRXUTNFEMI", "private_key"},
	{"scheme-less userinfo in a list", `{"credentials":["admin:sup3rS3cret@db.internal:5432"]}`, "sup3rS3cret", "credentials"},
	// Rows for the leaks the twenty-eighth review found: humps of one rune are
	// the joints of a name with none of its words, and a capitalised value is
	// not a variable name.
	{"a chunked credential in a span", "- **secret: aB3.xY9zQ7** is committed", "aB3.xY9zQ7", "is committed"},
	{"a capitalised credential in a span", `- **password: Summer2024Rocks** is committed`, "Summer2024Rocks", "is committed"},
	{"a capitalised credential in an array", `{"passwords":["Kj9mQv3xLp7nWd"]}`, "Kj9mQv3xLp7nWd", "passwords"},
	{"a lowercase hex path segment", `secret: uploads/a8f3d9e2c1b47f60a8f3d9e2c1b47f60a8f3d9e2.dat`, "a8f3d9e2c1b47f60", "secret"},
	{"a jwt in a span", "- **api_key: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc** is hardcoded", "eyJhbGciOiJIUzI1NiJ9", "is hardcoded"},
	{"a url with a password in a span", `- **token: https://user:s3cr3tpassw0rd@example.com/repo** is committed`, "s3cr3tpassw0rd", "is committed"},
	// An array element that is not itself a credential still goes through the
	// patterns: a scanner's list of findings is sentences with credentials in
	// them.
	{"a credential inside a listed sentence", `{"secrets":["Hardcoded key AKIAIOSFODNN7EXAMPLE in app.py"]}`, "AKIAIOSFODNN7EXAMPLE", "app.py"},
	{"a connection string in a list", `{"credentials":["postgres://admin:sup3rS3cret@db.internal:5432/app"]}`, "sup3rS3cret", "credentials"},
	// Rows for the leaks the twenty-ninth review found, and a second sample for
	// every class the rounds before it pinned by its literal. Each row above was
	// passing for a reason the class does not guarantee — one 32-rune piece, a
	// username with no dot in it — so each is doubled here with a value chosen
	// so it cannot pass by accident. Pinning the literal instead of the class
	// is what let six rounds trade one direction for the other.
	{"a chunked hex credential", `api_key: 0F8A2B_C4D6E8FA_0B2C4D6E_8FA0B2C4`, "0F8A2B", "api_key"},
	{"a chunked base64 credential", `AWS_SECRET_ACCESS_KEY: wJalrX_UtnFEMI_K7MDENG_bPxRfiCY`, "wJalrX", "AWS_SECRET_ACCESS_KEY"},
	{"a camelcase password in an assignment", `password: myPassword123`, "myPassword123", "password"},
	{"a leetspeak password in an assignment", `password: sup3rS3cret`, "sup3rS3cret", "password"},
	{"an identifier-shaped password in an assignment", `token: sessionToken2`, "sessionToken2", "token"},
	{"a dotted password in an assignment", `password: hunter2.trustno1`, "hunter2.trustno1", "password"},
	{"a camelcase password echoed from a config", `{"results":[{"extra":{"lines":"  password: dbHunter2pass"}}]}`, "dbHunter2pass", "results"},
	{"a dotted username in userinfo", `{"credentials":["first.last:s3cr3tpassw0rd@db.internal:5432"]}`, "s3cr3tpassw0rd", "credentials"},
	{"a dotted password in userinfo", `{"credentials":["admin:sup3r.S3cret@db.internal"]}`, "sup3r.S3cret", "credentials"},
	// Rows for the leaks the thirtieth review found. A bare `$` prefix is not a
	// variable expansion: it is also every crypt-format hash there is, which is
	// exactly what the field these sit under holds. And a long path segment
	// with no word break in it is a payload whether or not it is hex.
	{"a bcrypt hash", `password: $2b$12$eImiTXuWVxfM37uY4JANjQ9Xk0mGxYtQ`, "eImiTXuWVxfM", "password"},
	{"a bcrypt hash echoed from a config", `{"extra":{"lines":"password_hash: $2y$10$N9qo8uLOickgx2ZMRZoMye"}}`, "N9qo8uLOickgx2", "extra"},
	{"an argon2 hash", `password: $argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ`, "c29tZXNhbHQ", "password"},
	{"a md5-crypt hash", `password: $1$saltsalt$qJH7.N4xYta3aEG.dfeDMg`, "qJH7", "password"},
	{"a literal starting with a dollar", `password: $Tr0ub4dor3xK`, "Tr0ub4dor3xK", "password"},
	{"a lowercase base64 path segment", `secret: uploads/wjalrxutnfemik7mdengbpxrficyexamplekey.key`, "wjalrxutnfemi", "secret"},
	{"an unbroken path segment echoed from a config", `{"extra":{"lines":"secret: keys/k3jd8fh2ns0dkq9emxz7pq1wbv4rt6yuz.dat"}}`, "k3jd8fh2ns0dkq", "extra"},
	// Rows for the leaks the thirty-first review found. The crypt rule was
	// switched off wherever the line carried no other secret-ish word — the
	// hint has to be a superset of every pattern it gates — and the four rows
	// above all say "password", so none of them could see it.
	{"a crypt hash with no secret-ish name", `hash: $2b$12$eImiTXuWVxfM37uY4JANjQ9Xk0mGxYtQ`, "eImiTXuWVxfM", "hash"},
	{"a shadow line echoed from a file", `{"extra":{"lines":"root:$6$saltsalt$L9.uJ3xYta3aEG.dfeDMgQz:19000:0:99999:7:::"}}`, "L9.uJ3xYta3aEG", "extra"},
	// A reference is only a reference if what is in front of the credential's
	// name reads like a container. Testing the tail alone was weaker than the
	// name test it overrules, and it overrules the assignment syntax.
	{"a literal ending in a secret-ish word", `password: hunter2trustno1.password`, "hunter2trustno1", "password:"},
	{"a base64 value ending in a secret-ish word", `{"extra":{"lines":"  api_key: wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY.secret"}}`, "wJalrXUtnFEMI", "extra"},
	{"scheme-relative userinfo", `{"credentials":["//admin:sup3rS3cret@db.internal"]}`, "sup3rS3cret", "credentials"},
	// A document too deep to walk is handed to the patterns rather than
	// half-scrubbed: the credential goes, and the depth is not an excuse.
	{"a credential past the depth bound", deeplyNested(`{"password":"hunter2trustno1"}`, maxScrubDepth+200), "hunter2trustno1", "password"},
}

// deeplyNested wraps a document in n levels of array, which is what makes the
// structural pass run out of depth.
func deeplyNested(inner string, n int) string {
	return strings.Repeat(`[`, n) + inner + strings.Repeat(`]`, n)
}

// knownLimitations are the shapes this pattern set deliberately does not
// catch, asserted so the trade-off cannot drift without CI noticing. A comment
// alone let one of them be stated more narrowly than the code implemented.
var knownLimitations = []struct{ name, in, survives string }{
	// A path separator and a dotted extension together read as a path. The
	// two are not separable by shape, and of the errors available, reading a
	// cited path as a credential is the one that corrupts the corpus this
	// feature builds. See isFileReference.
	{"a slashed value with a dotted tail", `secret: wJalrX/K7MDENG/bPxRfiCYEXAMPLE.key`, "wJalrX"},
	// A short value ending in a dotted extension is a path even without a
	// separator, because `private_key: server.pem` and `api_key: settings.py`
	// are files a review names and requiring the separator rewrote both. The
	// cost is a password that happens to end that way. Past 24 runes with no
	// separator the length settles it and a JWT is redacted; see isFileReference.
	{"a short value with a dotted tail", `password: hunter2.key`, "hunter2.key"},
	// An array element under a secret-ish name is taken on its own shape, so a
	// list of rule names survives — and so does a credential shaped like one.
	{"an array of wordlike credentials", `{"passwords":["correcthorse"]}`, "correcthorse"},
	// A name with words before it needs a value that could be nothing else,
	// and "could be nothing else" is spelled "carries a digit".
	{"a wordless credential mid-sentence", `Using the password: correcthorse`, "correcthorse"},
	// A credential shaped like a *word-composed* qualified name survives:
	// pieces under sixteen runes, humps of two or more, no run of capitals, no
	// userinfo. The bound is what the class is — a secret can be spelled that
	// way and a name has to be — and the alternative rewrote every identifier
	// a review quotes that happens to carry a digit. See namesSomething.
	{"a word-shaped credential", "- **secret: dbHunter2.pass** is committed", "dbHunter2.pass"},
	// Past the depth bound the structural pass hands the whole text to the
	// patterns, and the patterns have no equivalent of the array rule — a name
	// followed by `[` is not a `name: value`. Nothing but nesting built to
	// reach the bound gets there.
	{"an array of credentials past the depth bound",
		deeplyNested(`{"passwords":["a8f3d9e2c1b47f60"]}`, maxScrubDepth+200), "a8f3d9e2c1b47f60"},
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
	// The same, single-quoted: the double-quote rules were bounded to a line
	// and the single-quote ones were not, so an apostrophe in the reviewer's
	// prose deleted every finding down to the next one.
	{"an unbalanced single quote after a name", "## Findings\n\n- secret='admin — see the note\n\n- The retry loop doesn't back off\n\n- The endpoint is unbounded\n"},
	// Code the reviewer quoted, and a value with words after it: an
	// assignment's line ends at the value, a sentence's does not.
	{"a struct field in prose", "- The struct sets Token:tokenValue without validation"},
	{"a section name after a colon", "Note the password:overview section"},
	{"a code fragment with prose after it", "apiKey:process.env.API_KEY is read at startup"},
	// A name with words before it is a sentence. Only a value that could be
	// nothing else — one token carrying a digit — is taken from one, so an
	// identifier the reviewer named is left as written whatever punctuation
	// follows it.
	{"a value followed by a clause", "- The api_key: abcdefghij, hardcoded in config.go, must move to env"},
	{"an identifier ending a sentence", "- Rename the token: sessionToken."},
	// A code span that a sentence continues past is a sentence about code, not
	// a bullet holding an assignment. The span that holds nothing but the
	// assignment is redacted; see beginsItsUnit.
	{"a code span a sentence continues past", "`token: sessionToken` is never validated"},
	// The span rule does not turn every quoted assignment into a credential:
	// inside the span the value still has to be one, and a cited path or an
	// identifier is not.
	{"a cited path in a span a sentence continues past", "- **The api_key: internal/auth/token.go** is unused"},
	{"a rule name listed under a secret-ish key", `{"secrets":["hardcoded-aws-key","generic-api-key"]}`},
	// Rows for the over-redaction the twenty-fifth review found. Bounding the
	// sentence at the span left the value to decide alone, and "one token with
	// a digit in it" describes most of the identifiers a review names.
	{"a qualified identifier in a span", "- `token: cfg.OAuth2Token` is never validated"},
	{"a constant name in a span", "- **secret: SHA256_DIGEST** is logged at startup"},
	{"an env lookup in a span", "- **api_key: process.env.API_KEY2** is read at boot"},
	{"a version in a span", "- **api_key: v1.2.3-rc1** is pinned in the lockfile"},
	// A path is short pieces however deep it goes, so a ceiling on the whole
	// value rewrote the ones a real project has.
	{"a deep cited path", "- The private_key: src/main/java/com/example/service/authentication/TokenServiceImpl.java is committed"},
	{"a scoped package path", "- The token: node_modules/@aws-sdk/client-secrets-manager/dist-cjs/index.js reads it"},
	{"a deep config path", "- The secret: packages/backend/src/config/environments/production/keys-v2.json holds it"},
	// A list under a secret-ish key holds names as often as values.
	{"variable names listed under a secret-ish key", `{"secrets":["OAUTH2_CLIENT_ID","DB_PASSWORD","STRIPE_KEY"]}`},
	{"a versioned rule id listed under a secret-ish key", `{"secrets":["gitleaks:generic-api-key:v8.18.0"]}`},
	{"an endpoint listed under a secret-ish key", `{"tokens":["https://api.example.com/v2/tokens"]}`},
	// A long file name is a name; what makes a long path segment a payload is
	// digits mixed through it. Bounding every long segment rewrote the test
	// classes a Java or TypeScript suite is full of.
	{"a long cited test file", "- The private_key: src/test/AuthenticationTokenProviderTest.java is committed"},
	{"a long cited chunk file", "- The token: dist/static/js/VendorAuthenticationBundleChunk.js reads it"},
	// An acronym inside a long file name is still a name, and a value that
	// stands in for the credential is not the credential.
	{"an acronym in a long cited path", "- The private_key: config/certificates/ProductionAPIGatewayCertificate.pem is committed"},
	{"a shell env indirection", `password: $DB_PASSWORD`},
	{"a braced env indirection", `password: ${POSTGRES_PASSWORD}`},
	{"an env lookup echoed from a config", `{"extra":{"lines":"api_key: process.env.API_KEY"}}`},
	{"a package coordinate in a list", `{"secrets":["com.example:lib:1.2.3@aar"]}`},
	// A long file name carries a digit as often as not; banning them rewrote
	// the classes a real project has. These are the line-start form on purpose:
	// with words after the value, followedByWords decides them and the path
	// rule is never reached, so the mid-sentence versions pinned nothing.
	{"a digit in a long cited path", "private_key: src/main/UserAuthenticationTokenProviderFactoryImpl2.java"},
	{"a digit in a long component path", "secret: packages/web/src/components/Auth2FactorEnrollmentDialogContainer.tsx"},
	// An image reference has one colon and an `@`, and is not a login.
	{"an image digest reference", `{"secrets":["docker.io/library/nginx:1.2@sha256"]}`},
	{"a registry digest reference", `{"tokens":["registry.io/ns/img:v1@sha256"]}`},
	// A value that points at the field holding the credential is not the
	// credential, wherever it appears — and where it appears most is a scanner
	// echoing the source line it flagged.
	{"a config field echoed from source", `{"extra":{"lines":"  password: cfg.DBPassword"}}`},
	{"a config struct field echoed from source", `{"extra":{"lines":"  api_key: dbConfig.password"}}`},
	{"a settings lookup echoed from source", `{"extra":{"lines":"  secret: settings.API_KEY"}}`},
	{"a config field in a bare span", "- `token: cfg.OAuth2Token`"},
	// A variable expansion is what a shell, a compose file and PowerShell each
	// write, defaults included — and the unquoted value class stops at the
	// closing brace, so what arrives is missing it.
	{"an expansion with a default", `password: ${DB_PASSWORD:-changeme}`},
	{"a powershell environment drive", `api_key: $env:API_KEY`},
	{"a camelcase variable", `password: $dbPassword`},
	// A replacement template is a command a review quotes, not a hash.
	{"a regex replacement template", `sed -e 's/(a)(b)/$1$2/' internal/auth/token.go`},
	// Go and Kubernetes write long file names with no break in them at all;
	// what that convention does not do is mix digits in.
	{"an unbroken go file name", "private_key: k8s.io/api/admissionregistration/validatingwebhookconfiguration.go"},
	{"a rule's own remediation text", `{"results":[{"extra":{"message":"Detected a hardcoded password: change_me_now"}}]}`},
	// A colon at the end of a line, and a report that quotes a key's header
	// line mid-sentence: both had every finding after them deleted.
	{"a heading ending in a colon", "## Findings\n\n### 1. Hardcoded credential:\n\nThe handler compares the value directly.\n\n### 2. Missing rate limit\n\nThe endpoint is unbounded.\n"},
}

// acceptedRewrites are the reviewer's own words this deliberately rewrites —
// the knownLimitations list in the other direction, and asserted for the same
// reason: an over-redaction that nothing pins is one nobody notices.
//
// A span holding nothing but `name: value`, with no sentence after it, is
// indistinguishable from the assignment it quotes: "`api_key: abcdefghijkl`"
// and "- **secret: SHA256_DIGEST**" are the same shape, and the corpus requires
// the first to be redacted. The direction is chosen: a credential emitted into
// a committed artifact reads exactly like a clean scan, while a rewritten
// identifier is visible in the artifact next to the finding it belongs to.
//
// What is left here is narrow. A value that *points* at a credential —
// `cfg.OAuth2Token`, `settings.API_KEY`, `$DB_PASSWORD` — is recognised as
// such wherever it appears, including in an `extra.lines` echo, which is the
// commonest text this package sees. This row is a constant *name* used as a
// value, which nothing distinguishes from a value; and once there is a
// sentence after the span, the name survives anyway, which the rows above
// pin.
var acceptedRewrites = []struct{ name, in, rewritten string }{
	{"a constant in a bare span", "- **secret: SHA256_DIGEST**", "SHA256_DIGEST"},
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

// The trade-offs, pinned. A change that starts catching one of these is
// welcome — and has to say so here rather than passing silently.
func TestRedactSecrets_knownLimitations(t *testing.T) {
	for _, tc := range knownLimitations {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSecrets(tc.in); !strings.Contains(got, tc.survives) {
				t.Errorf("this is now caught — update knownLimitations:\n  in:  %q\n  out: %q", tc.in, got)
			}
		})
	}
}

// The other direction of the same discipline: a change that stops rewriting one
// of these is welcome, and has to say so here rather than passing silently.
func TestRedactSecrets_acceptedRewrites(t *testing.T) {
	for _, tc := range acceptedRewrites {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSecrets(tc.in); strings.Contains(got, tc.rewritten) {
				t.Errorf("this is now left alone — move it to proseCorpus:\n  in:  %q\n  out: %q", tc.in, got)
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

// The corpus row for a deep document passes either way — the patterns catch
// the credential too — so the handover itself is asserted here. A structural
// pass that stops short has scrubbed only part of the document, and reporting
// that as a completed scrub is what would leave the rest of it committed.
func TestRedactJSON_abandonsADocumentTooDeepToWalk(t *testing.T) {
	shallow := `{"a":{"password":"hunter2trustno1"}}`
	if _, ok := redactJSON(shallow); !ok {
		t.Fatal("a shallow document should be handled structurally")
	}
	if _, ok := redactJSON(deeplyNested(shallow, maxScrubDepth+200)); ok {
		t.Error("a document past the depth bound should fall through to the patterns")
	}
	// The bound is on nesting, not on size: a wide document is still walked.
	if _, ok := redactJSON(`[` + strings.Repeat(`"x",`, 5000) + `"y"]`); !ok {
		t.Error("a wide document should still be handled structurally")
	}
}
