package detect

import "testing"

func TestParseGitHubURL(t *testing.T) {
	cases := []struct {
		input      string
		wantOwner  string
		wantRepo   string
		wantErrMsg string
	}{
		{"git@github.com:ButtersesHouse/Chalmuns.git", "ButtersesHouse", "Chalmuns", ""},
		{"https://github.com/ButtersesHouse/Chalmuns.git", "ButtersesHouse", "Chalmuns", ""},
		{"https://github.com/ButtersesHouse/Chalmuns", "ButtersesHouse", "Chalmuns", ""},
		{"https://github.com/org/repo.git", "org", "repo", ""},
		{"git@gitlab.com:org/repo.git", "", "", "not a GitHub remote"},
	}

	for _, tc := range cases {
		owner, repo, err := parseGitHubURL(tc.input)
		if tc.wantErrMsg != "" {
			if err == nil {
				t.Errorf("%q: expected error containing %q, got nil", tc.input, tc.wantErrMsg)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.input, err)
			continue
		}
		if owner != tc.wantOwner || repo != tc.wantRepo {
			t.Errorf("%q: want %s/%s, got %s/%s", tc.input, tc.wantOwner, tc.wantRepo, owner, repo)
		}
	}
}
