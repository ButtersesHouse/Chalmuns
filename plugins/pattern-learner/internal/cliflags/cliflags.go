// Package cliflags is the one flag parser every subcommand uses.
//
// It exists because there were two. cmd/pattern-learner had its own
// copy and internal/pipeline had another, and when the first was
// taught to read "--flag=value" and to reject an unknown flag, the
// second was not. The result was that `write-outputs --skills-dir=DIR`
// worked while `extract-lean --prs=1,2` silently dropped the flag and
// processed the entire PR cache, and a misspelled flag was a no-op in
// half the CLI. Two copies of an argument parser will drift again, so
// there is now one, and both callers import it.
package cliflags

import (
	"fmt"
	"strings"
)

// Value extracts a flag's value from args, accepting both "--flag value"
// and "--flag=value". Both forms are read because a caller who writes the
// equals form has every reason to expect it to work: it is the form Go's
// own flag package accepts, and the one an agent following prose
// documentation is most likely to produce.
func Value(args []string, flag, def string) string {
	prefix := flag + "="
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return def
}

// Has reports whether a boolean flag appears in args.
func Has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// Check rejects any argument that looks like a flag this subcommand does
// not know. Silently ignoring a typo is how `--skils-dir /somewhere`
// became a run that wrote to, and pruned, the default skills directory
// while reporting success; the same silence made `--prs=1,2` process
// every PR in the cache. A named refusal costs one rerun; a silent
// misread costs whatever the wrong default touched.
func Check(args, valueFlags, boolFlags []string) error {
	known := map[string]bool{}
	takesValue := map[string]bool{}
	for _, f := range valueFlags {
		known[f], takesValue[f] = true, true
	}
	for _, f := range boolFlags {
		known[f] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			continue // a positional argument, or a value consumed below
		}
		name := a
		inline := false
		if eq := strings.Index(a, "="); eq >= 0 {
			name, inline = a[:eq], true
		}
		if !known[name] {
			return fmt.Errorf("unknown flag %q; this subcommand accepts %s", name,
				strings.Join(append(append([]string{}, valueFlags...), boolFlags...), ", "))
		}
		if takesValue[name] {
			if inline {
				continue
			}
			if i+1 >= len(args) {
				return fmt.Errorf("flag %s needs a value", name)
			}
			i++ // skip the value so it is not read as a flag
		}
	}
	return nil
}
