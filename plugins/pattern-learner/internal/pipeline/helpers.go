package pipeline

import "github.com/ButtersesHouse/Chalmuns/internal/cliflags"

// The pipeline subcommands share the CLI's one flag parser. They used to
// carry their own copy, which read only the "--flag value" form and
// rejected nothing: `extract-lean --prs=1,2` silently became "no --prs"
// and processed every PR in the cache.
func flagVal(args []string, flag, def string) string {
	return cliflags.Value(args, flag, def)
}

func hasFlag(args []string, flag string) bool {
	return cliflags.Has(args, flag)
}

func checkFlags(args, valueFlags, boolFlags []string) error {
	return cliflags.Check(args, valueFlags, boolFlags)
}
