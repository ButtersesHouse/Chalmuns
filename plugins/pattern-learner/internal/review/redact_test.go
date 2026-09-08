package review

import (
	"strings"
	"testing"
)

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
