package trconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "talkgroups.csv")
	write := func(content string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	write("100,1,Fire\n", base)

	start, err := StatFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := NewFileWatch(path, start)
	if _, ok := w.Poll(); ok {
		t.Fatal("unchanged file reported as changed")
	}

	// A change is reported once it has been stable for one poll.
	write("100,1,Fire Dispatch\n", base.Add(time.Minute))
	if _, ok := w.Poll(); ok {
		t.Fatal("change reported on the first poll after it (file may still be written)")
	}
	stamp, ok := w.Poll()
	if !ok {
		t.Fatal("stable change not reported")
	}
	// Until it is marked imported, it keeps being reported (retry).
	if _, ok := w.Poll(); !ok {
		t.Fatal("change not reported again before Imported")
	}
	w.Imported(stamp)
	if _, ok := w.Poll(); ok {
		t.Fatal("imported version reported again")
	}

	// A file that keeps changing is not reported until it settles.
	write("100,1,A\n", base.Add(2*time.Minute))
	if _, ok := w.Poll(); ok {
		t.Fatal("reported while changing (1)")
	}
	write("100,1,AB\n", base.Add(3*time.Minute))
	if _, ok := w.Poll(); ok {
		t.Fatal("reported while changing (2)")
	}
	if stamp, ok = w.Poll(); !ok || stamp.Size != int64(len("100,1,AB\n")) {
		t.Fatalf("settled change: ok=%v stamp=%+v", ok, stamp)
	}
	w.Imported(stamp)

	// A same-size edit is detected by its modification time.
	write("100,1,AC\n", base.Add(4*time.Minute))
	w.Poll()
	if _, ok := w.Poll(); !ok {
		t.Fatal("same-size edit with a new mtime not reported")
	}

	// A missing file is never ready; it is picked up again once it reappears.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Poll(); ok {
		t.Fatal("missing file reported as ready")
	}
	write("100,1,Back\n", base.Add(5*time.Minute))
	w.Poll()
	if _, ok := w.Poll(); !ok {
		t.Fatal("reappeared file not reported")
	}
}

func TestFileWatch_MissingAtStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unitTags.csv")
	w := NewFileWatch(path, FileStamp{})
	if _, ok := w.Poll(); ok {
		t.Fatal("missing file reported")
	}
	if err := os.WriteFile(path, []byte("1001,Engine 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.Poll()
	if _, ok := w.Poll(); !ok {
		t.Fatal("file created after start not reported")
	}
}

// A change made while the file was being read at startup (after the stamp was
// taken) is picked up.
func TestFileWatch_ChangedWhileImporting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "talkgroups.csv")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, []byte("100,1,Fire\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, base, base); err != nil {
		t.Fatal(err)
	}
	stamp, err := StatFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("100,1,Fire Dispatch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := NewFileWatch(path, stamp)
	w.Poll()
	if _, ok := w.Poll(); !ok {
		t.Fatal("change made after the stamp was taken not reported")
	}
}
