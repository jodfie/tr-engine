package trconfig

import (
	"os"
	"time"
)

// FileStamp identifies a version of a file by its size and modification time.
type FileStamp struct {
	Size    int64
	ModTime time.Time
}

// Equal reports whether s and o identify the same version of a file.
func (s FileStamp) Equal(o FileStamp) bool {
	return s.Size == o.Size && s.ModTime.Equal(o.ModTime)
}

// StatFile returns the current FileStamp of path.
func StatFile(path string) (FileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FileStamp{}, err
	}
	return FileStamp{Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// FileWatch detects changes to a file (e.g. one of trunk-recorder's CSVs) by
// polling its FileStamp. Polling works where filesystem notifications don't
// (network mounts, Docker Desktop bind mounts) and survives editors that save
// by replacing the file.
type FileWatch struct {
	Path     string
	imported FileStamp // version last imported
	seen     FileStamp // version seen by the previous Poll
}

// NewFileWatch starts watching path. imported is the version already imported:
// its StatFile stamp taken before the file was read, so a change made while it
// was being read is picked up. A zero stamp (file missing) makes the file
// picked up once it appears.
func NewFileWatch(path string, imported FileStamp) *FileWatch {
	return &FileWatch{Path: path, imported: imported, seen: imported}
}

// Poll reports whether a changed version of the file is ready to import: its
// stamp differs from the imported version and is the same as at the previous
// Poll, so a file that is still being written is not read half-way (a change
// is picked up on the second poll after it). A file that can't be stat'ed is
// never ready. Call Imported with the returned stamp once it is handled.
func (w *FileWatch) Poll() (FileStamp, bool) {
	stamp, err := StatFile(w.Path)
	if err != nil {
		return FileStamp{}, false
	}
	stable := stamp.Equal(w.seen)
	w.seen = stamp
	return stamp, stable && !stamp.Equal(w.imported)
}

// Imported records stamp as the imported version, so Poll reports the file
// again only after it changes.
func (w *FileWatch) Imported(stamp FileStamp) {
	w.imported = stamp
}
