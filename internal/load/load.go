package load

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"

	"ci-profiler/internal/model"
)

type pair struct {
	run     int64
	attempt int
}

// loader holds the budget for one Load call. It is used by a single goroutine.
type loader struct {
	root      *os.Root
	lim       Limits
	remaining int64
	entries   int
	errs      []error
}

// errFatal marks a cap error that stops the load at once.
var errFatal = errors.New("input limit exceeded")

func (l *loader) addErr(path string, err error) bool {
	return l.fail(&FileError{Path: path, Err: err})
}

// fail records an error and reports whether loading may continue.
func (l *loader) fail(err error) bool {
	l.errs = append(l.errs, err)
	return len(l.errs) < l.lim.MaxErrors
}

// list returns the sorted .json file names in sub. Every entry counts toward MaxFiles, in
// batches, so a huge directory is never fully listed. Non-regular entries are errors.
func (l *loader) list(sub string, required bool) ([]string, error) {
	fi, err := l.root.Lstat(sub)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !required {
			return nil, nil
		}
		return nil, &FileError{Path: sub, Err: err}
	}
	if !fi.IsDir() {
		return nil, &FileError{Path: sub, Err: errors.New("must be a real directory (not a symlink or file)")}
	}
	d, err := l.root.OpenFile(sub, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, &FileError{Path: sub, Err: err}
	}
	defer d.Close()
	var names []string
	for {
		batch, err := d.ReadDir(100)
		for _, e := range batch {
			l.entries++
			if l.entries > l.lim.MaxFiles {
				return nil, fmt.Errorf("%w: more than %d entries in runs/ and jobs/", errFatal, l.lim.MaxFiles)
			}
			p := path.Join(sub, e.Name())
			if !e.Type().IsRegular() {
				if !l.addErr(p, errors.New("not a regular file (symlinks, directories and special files are rejected)")) {
					return nil, errFatal
				}
				continue
			}
			if strings.HasSuffix(e.Name(), ".json") {
				names = append(names, p)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &FileError{Path: sub, Err: err}
		}
	}
	sort.Strings(names)
	return names, nil
}

// read opens name through the root without blocking, checks it is a regular file, and reads at
// most the smaller of the per-file cap and the remaining total budget.
func (l *loader) read(name string) ([]byte, error) {
	f, err := l.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, &FileError{Path: name, Err: err}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, &FileError{Path: name, Err: err}
	}
	if !fi.Mode().IsRegular() {
		return nil, &FileError{Path: name, Err: errors.New("not a regular file")}
	}
	limit := l.lim.MaxFileBytes
	totalBound := false
	if l.remaining < limit {
		limit, totalBound = l.remaining, true
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	l.remaining -= int64(len(data)) // count what was really read, even on error or overrun
	if err != nil {
		return nil, &FileError{Path: name, Err: err}
	}
	if int64(len(data)) > limit {
		if totalBound {
			return nil, fmt.Errorf("%w: total input exceeds %d bytes at %s", errFatal, l.lim.MaxTotalBytes, name)
		}
		return nil, &FileError{Path: name, Err: fmt.Errorf("file exceeds the %d byte limit", l.lim.MaxFileBytes)}
	}
	return data, nil
}

func (l *loader) finish(err error) error {
	if err != nil {
		l.errs = append(l.errs, err)
	}
	return errors.Join(l.errs...)
}

// Load reads dir/runs/*.json, dir/jobs/*.json and the optional dir/needs.json. It returns every
// problem it found (up to Limits.MaxErrors) as joined errors naming the files.
func Load(dir string, lim Limits) (*model.Dataset, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	l := &loader{root: root, lim: lim, remaining: lim.MaxTotalBytes}

	runNames, err := l.list("runs", true)
	if err != nil {
		return nil, l.finish(err)
	}
	jobNames, err := l.list("jobs", false)
	if err != nil {
		return nil, l.finish(err)
	}

	runs := map[pair]model.Run{}
	for _, n := range runNames {
		data, err := l.read(n)
		if err != nil {
			if errors.Is(err, errFatal) {
				return nil, l.finish(err)
			}
			if !l.fail(err) {
				return nil, l.finish(nil)
			}
			continue
		}
		r, err := ParseRun(data)
		if err != nil {
			if !l.addErr(n, err) {
				return nil, l.finish(nil)
			}
			continue
		}
		r.File = n
		k := pair{r.ID, r.Attempt}
		if prev, dup := runs[k]; dup {
			if !l.addErr(n, fmt.Errorf("duplicate run %d attempt %d, also in %s", r.ID, r.Attempt, prev.File)) {
				return nil, l.finish(nil)
			}
			continue
		}
		runs[k] = r
	}

	jobs := map[pair][]model.Job{}
	jobFile := map[pair]string{}
	for _, n := range jobNames {
		data, err := l.read(n)
		if err != nil {
			if errors.Is(err, errFatal) {
				return nil, l.finish(err)
			}
			if !l.fail(err) {
				return nil, l.finish(nil)
			}
			continue
		}
		js, err := ParseJobs(data, lim)
		if err != nil {
			if !l.addErr(n, err) {
				return nil, l.finish(nil)
			}
			continue
		}
		k := pair{js[0].RunID, js[0].Attempt}
		if prev, dup := jobFile[k]; dup {
			if !l.addErr(n, fmt.Errorf("duplicate jobs for run %d attempt %d, also in %s", k.run, k.attempt, prev)) {
				return nil, l.finish(nil)
			}
			continue
		}
		if _, ok := runs[k]; !ok {
			if !l.addErr(n, fmt.Errorf("no run file for run %d attempt %d", k.run, k.attempt)) {
				return nil, l.finish(nil)
			}
			continue
		}
		jobs[k], jobFile[k] = js, n
	}

	ds := &model.Dataset{}
	if data, err := l.read("needs.json"); err == nil {
		if ds.Needs, err = ParseNeeds(data, lim); err != nil {
			l.addErr("needs.json", err)
		}
	} else if errors.Is(err, errFatal) {
		return nil, l.finish(err)
	} else if !isNotExist(err) {
		l.fail(err)
	}

	if len(l.errs) > 0 {
		return nil, l.finish(nil)
	}
	for k, r := range runs {
		js, ok := jobs[k]
		ds.Attempts = append(ds.Attempts, model.Attempt{Run: r, Jobs: js, HasJobs: ok})
	}
	sort.Slice(ds.Attempts, func(i, j int) bool {
		a, b := ds.Attempts[i].Run, ds.Attempts[j].Run
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Attempt < b.Attempt
	})
	return ds, nil
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
