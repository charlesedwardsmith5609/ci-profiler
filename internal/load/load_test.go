package load

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ci-profiler/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

const sample = "../../testdata/sample"

func summary(ds *model.Dataset) string {
	var b strings.Builder
	for _, a := range ds.Attempts {
		r := a.Run
		fmt.Fprintf(&b, "run %d attempt %d wf=%s %s/%s jobs=%v file=%s\n", r.ID, r.Attempt, r.WorkflowKey(), r.Status, r.Conclusion, a.HasJobs, r.File)
		for _, j := range a.Jobs {
			fmt.Fprintf(&b, "  job %q %s start=%s end=%s steps=%d\n", j.Name, j.Conclusion,
				j.StartedAt.Format(time.RFC3339), j.CompletedAt.Format(time.RFC3339), len(j.Steps))
		}
	}
	for _, wf := range sortedKeys(ds.Needs) {
		for _, j := range sortedKeys(ds.Needs[wf]) {
			fmt.Fprintf(&b, "needs %s %s -> %v\n", wf, j, ds.Needs[wf][j])
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	for i := range k {
		for j := i + 1; j < len(k); j++ {
			if k[j] < k[i] {
				k[i], k[j] = k[j], k[i]
			}
		}
	}
	return k
}

func TestLoadSampleGolden(t *testing.T) {
	ds, err := Load(sample, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	got := summary(ds)
	golden := "../../testdata/golden/load-sample.txt"
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// tree writes files (relative path -> content) into a fresh temp dir.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const (
	run1  = `{"id":1,"run_attempt":1,"workflow_id":5}`
	jobs1 = `{"jobs":[{"id":10,"run_id":1,"run_attempt":1,"name":"a"}]}`
)

func TestLoadErrors(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxFileBytes = 100
	tests := []struct {
		name    string
		files   map[string]string
		lim     *Limits
		wantErr string
	}{
		{"ok", map[string]string{"runs/a.json": run1, "jobs/a.json": jobs1}, nil, ""},
		{"ok without jobs", map[string]string{"runs/a.json": run1}, nil, ""},
		{"missing runs dir", map[string]string{"jobs/a.json": jobs1}, nil, "runs"},
		{"malformed run", map[string]string{"runs/a.json": `{"id":`}, nil, "runs/a.json"},
		{"oversized file", map[string]string{"runs/a.json": run1 + strings.Repeat(" ", 200)}, &lim, "byte limit"},
		{"duplicate runs", map[string]string{"runs/a.json": run1, "runs/b.json": run1}, nil, "duplicate run 1 attempt 1, also in runs/a.json"},
		{"duplicate jobs", map[string]string{"runs/a.json": run1, "jobs/a.json": jobs1, "jobs/b.json": jobs1}, nil, "duplicate jobs"},
		{"jobs without run", map[string]string{"runs/a.json": run1, "jobs/a.json": strings.ReplaceAll(jobs1, `"run_id":1`, `"run_id":2`)}, nil, "no run file"},
		{"empty jobs", map[string]string{"runs/a.json": run1, "jobs/a.json": `{"jobs":[]}`}, nil, "cannot be linked"},
		{"name does not link", map[string]string{"runs/1-1.json": run1, "jobs/9-9.json": strings.ReplaceAll(jobs1, `"run_id":1`, `"run_id":9`)}, nil, "no run file"},
		{"bad needs", map[string]string{"runs/a.json": run1, "needs.json": `[1]`}, nil, "needs.json"},
		{"directory named x.json", map[string]string{"runs/a.json": run1, "runs/x.json/inner": "x"}, nil, "not a regular file"},
		{"non-json ignored", map[string]string{"runs/a.json": run1, "runs/notes.txt": "hi"}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := DefaultLimits()
			if tc.lim != nil {
				l = *tc.lim
			}
			_, err := Load(tree(t, tc.files), l)
			checkErr(t, err, tc.wantErr)
		})
	}
}

func TestLoadCaps(t *testing.T) {
	t.Run("file count counts ignored entries", func(t *testing.T) {
		files := map[string]string{"runs/a.json": run1}
		for i := 0; i < 5; i++ {
			files[fmt.Sprintf("runs/n%d.txt", i)] = "x"
		}
		lim := DefaultLimits()
		lim.MaxFiles = 3
		_, err := Load(tree(t, files), lim)
		checkErr(t, err, "more than 3 entries")
	})
	t.Run("total bytes", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < 5; i++ {
			files[fmt.Sprintf("runs/%d.json", i)] = fmt.Sprintf(`{"id":%d,"run_attempt":1,"workflow_id":5}`, i+1) + strings.Repeat(" ", 100)
		}
		lim := DefaultLimits()
		lim.MaxTotalBytes = 250
		_, err := Load(tree(t, files), lim)
		checkErr(t, err, "total input exceeds")
	})
	t.Run("error count stops early", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < 30; i++ {
			files[fmt.Sprintf("runs/%02d.json", i)] = "{"
		}
		lim := DefaultLimits()
		lim.MaxErrors = 3
		_, err := Load(tree(t, files), lim)
		checkErr(t, err, "runs/00.json")
		if n := strings.Count(err.Error(), "runs/"); n != 3 {
			t.Errorf("reported %d errors, want 3: %v", n, err)
		}
	})
}

func TestLoadOversizedFilesCountTowardTotal(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 20; i++ {
		files[fmt.Sprintf("runs/%02d.json", i)] = strings.Repeat(" ", 100)
	}
	lim := DefaultLimits()
	lim.MaxFileBytes, lim.MaxTotalBytes = 100-1, 250
	_, err := Load(tree(t, files), lim)
	checkErr(t, err, "total input exceeds")
}

func TestLoadDuplicateErrorsHonorCap(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 30; i++ {
		files[fmt.Sprintf("runs/%02d.json", i)] = run1
	}
	lim := DefaultLimits()
	lim.MaxErrors = 3
	_, err := Load(tree(t, files), lim)
	checkErr(t, err, "duplicate run")
	if n := strings.Count(err.Error(), "duplicate run"); n != 3 {
		t.Errorf("reported %d errors, want 3", n)
	}
}

func TestLoadSymlinks(t *testing.T) {
	outside := tree(t, map[string]string{"secret.json": run1})
	dir := tree(t, map[string]string{"runs/real.json": run1})
	if err := os.Symlink(filepath.Join(outside, "secret.json"), filepath.Join(dir, "runs", "link.json")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	_, err := Load(dir, DefaultLimits())
	checkErr(t, err, "runs/link.json")

	t.Run("symlink inside root is rejected too", func(t *testing.T) {
		d := tree(t, map[string]string{"runs/real.json": run1})
		if err := os.Symlink("real.json", filepath.Join(d, "runs", "alias.json")); err != nil {
			t.Skip(err)
		}
		_, err := Load(d, DefaultLimits())
		checkErr(t, err, "not a regular file")
	})
	t.Run("runs dir is a symlink", func(t *testing.T) {
		d := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(d, "runs")); err != nil {
			t.Skip(err)
		}
		_, err := Load(d, DefaultLimits())
		checkErr(t, err, "real directory")
	})
	t.Run("needs.json symlink outside root", func(t *testing.T) {
		d := tree(t, map[string]string{"runs/a.json": run1})
		if err := os.Symlink(filepath.Join(outside, "secret.json"), filepath.Join(d, "needs.json")); err != nil {
			t.Skip(err)
		}
		_, err := Load(d, DefaultLimits())
		if err == nil {
			t.Fatal("symlinked needs.json outside the root was read")
		}
	})
}

func TestLoadFIFODoesNotHang(t *testing.T) {
	dir := tree(t, map[string]string{"runs/a.json": run1})
	if err := syscall.Mkfifo(filepath.Join(dir, "runs", "block.json"), 0o644); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	done := make(chan error, 1) // buffered so the goroutine can always exit
	go func() { _, err := Load(dir, DefaultLimits()); done <- err }()
	select {
	case err := <-done:
		checkErr(t, err, "not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("Load hung on a FIFO")
	}
}

func TestLoadNeedsAndOrder(t *testing.T) {
	dir := tree(t, map[string]string{
		"runs/z.json": `{"id":2,"run_attempt":1,"workflow_id":5}`,
		"runs/a.json": `{"id":1,"run_attempt":2,"workflow_id":5}`,
		"runs/b.json": `{"id":1,"run_attempt":1,"workflow_id":5}`,
		"needs.json":  `{"wf":{"b":["a"]}}`,
	})
	ds, err := Load(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range ds.Attempts {
		got = append(got, fmt.Sprintf("%d-%d", a.Run.ID, a.Run.Attempt))
	}
	if strings.Join(got, ",") != "1-1,1-2,2-1" {
		t.Errorf("order = %v", got)
	}
	if len(ds.Needs["wf"]["b"]) != 1 {
		t.Errorf("needs = %v", ds.Needs)
	}
}

func TestFileErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	if !errors.Is(&FileError{Path: "x", Err: inner}, inner) {
		t.Error("FileError does not unwrap")
	}
}
