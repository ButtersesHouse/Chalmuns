package fsatomic

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// os.CreateTemp always creates with 0600, and renaming that over a file
// carries the restrictive mode with it. state.json and the review artifacts
// live in a checkout that may be read by another user or a CI step, so an
// existing file must keep the mode it has.
func TestWriteFile_preservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0644 {
		t.Errorf("mode: want 0644, got %v", fi.Mode().Perm())
	}
}

func TestWriteFile_newFileGetsDefaultMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.json")
	if err := WriteFile(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != DefaultMode {
		t.Errorf("mode: want %v, got %v", DefaultMode, fi.Mode().Perm())
	}
}

// A shared temp name lets two writers' bytes interleave into a permanently
// corrupt file. Every writer must end up with a whole document.
func TestWriteFile_concurrentWritersNeverCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	short := []byte(`{"n":1}`)
	long := []byte(`{"n":2,"padding":"` + string(make([]byte, 4096)) + `"}`)

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := short
			if i%2 == 0 {
				data = long
			}
			if err := WriteFile(path, data); err != nil {
				t.Errorf("write: %v", err)
			}
		}(i)
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(short) && string(got) != string(long) {
		t.Errorf("file is neither document whole — %d bytes", len(got))
	}
	// No temp files left behind.
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
