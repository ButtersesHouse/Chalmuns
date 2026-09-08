// Package fsatomic is the one atomic file write this binary uses.
//
// It exists for the reason internal/cliflags exists: there were two copies.
// state.Write and review.WriteArtifact each grew the same
// create-temp / write / close / rename dance, with the same three cleanup
// paths, added in the same change for the same reason — and the codebase has
// already paid once for letting two copies of one routine drift apart. Any
// later fix here (an fsync before the rename, a retry across devices) is now
// made once.
package fsatomic

import (
	"os"
	"path/filepath"
)

// DefaultMode is the permission a file written through this package gets when
// it does not already exist.
//
// It is stated explicitly because os.CreateTemp always creates with 0600, and
// renaming that over a file carries the restrictive mode with it. Both files
// written here — state.json and the review artifacts — live in a checkout that
// may be shared with other users or read by a CI step under another account,
// and silently tightening their permissions would fail those readers with
// nothing to explain why.
const DefaultMode os.FileMode = 0644

// WriteFile writes data to path atomically: into a uniquely-named temporary
// file in the same directory, then renamed into place. The temp name is unique
// per writer, not derived from the target, because two processes writing one
// path is ordinary here — a capture hook firing while a pipeline run writes,
// two designations changed at once — and a shared temp name lets their bytes
// interleave into a permanently corrupt file.
//
// An existing file keeps the mode it already has; a new one gets DefaultMode.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	mode := DefaultMode
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
