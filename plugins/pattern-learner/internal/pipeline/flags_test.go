package pipeline

import "testing"

// The pipeline subcommands carried their own flag parser, which read only
// the "--flag value" form and rejected nothing. `extract-lean --prs=1,2`
// therefore became "no --prs given" and processed every PR in the cache,
// and a misspelled flag was a silent no-op. Both now go through the one
// shared parser in internal/cliflags.
func TestPipelineFlagsAcceptEqualsFormAndRejectTypos(t *testing.T) {
	if got := flagVal([]string{"--prs=1,2"}, "--prs", ""); got != "1,2" {
		t.Errorf("--prs=1,2 must be read as %q, got %q", "1,2", got)
	}
	if got := flagVal([]string{"--prs", "1,2"}, "--prs", ""); got != "1,2" {
		t.Errorf("the space form must still work, got %q", got)
	}
	if err := checkFlags([]string{"--cache-dirr", "/x"}, []string{"--cache-dir"}, nil); err == nil {
		t.Error("a misspelled flag must be refused, not silently dropped")
	}
	if err := checkFlags([]string{"--cache-dir=/x"}, []string{"--cache-dir"}, nil); err != nil {
		t.Errorf("the equals form must be accepted: %v", err)
	}
}

// Every pipeline subcommand validates its flags. A subcommand added later
// without a checkFlags call is exactly how this defect survived the first
// pass: write-outputs and promote were fixed, these four were missed.
func TestEveryPipelineSubcommandRejectsUnknownFlags(t *testing.T) {
	for name, run := range map[string]func([]string) error{
		"extract-lean":     RunExtractLean,
		"verify-grounding": RunVerifyGrounding,
		"classify":         RunClassify,
		"triage":           RunTriage,
	} {
		err := run([]string{"--definitely-not-a-flag", "x"})
		if err == nil {
			t.Errorf("%s accepted an unknown flag", name)
			continue
		}
		if got := err.Error(); got == "" || !contains(got, "unknown flag") {
			t.Errorf("%s must name the unknown flag, got %q", name, got)
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
