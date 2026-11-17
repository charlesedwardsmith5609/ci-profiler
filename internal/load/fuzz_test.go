package load

import (
	"os"
	"path/filepath"
	"testing"
)

func seed(f *testing.F, pattern string) {
	files, _ := filepath.Glob(filepath.Join(sample, pattern))
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[[[[[[[[[[`))
}

func FuzzParseRun(f *testing.F) {
	seed(f, "runs/*.json")
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := ParseRun(data)
		if err == nil && (r.ID <= 0 || r.Attempt < 1 || r.WorkflowKey() == "") {
			t.Fatalf("accepted invalid run %+v", r)
		}
	})
}

func FuzzParseJobs(f *testing.F) {
	seed(f, "jobs/*.json")
	lim := DefaultLimits()
	f.Fuzz(func(t *testing.T, data []byte) {
		jobs, err := ParseJobs(data, lim)
		if err != nil {
			return
		}
		if len(jobs) == 0 || len(jobs) > lim.MaxJobs {
			t.Fatalf("accepted %d jobs", len(jobs))
		}
		names := map[string]bool{}
		for _, j := range jobs {
			if j.RunID != jobs[0].RunID || j.Attempt != jobs[0].Attempt || names[j.Name] || len(j.Steps) > lim.MaxSteps {
				t.Fatalf("accepted inconsistent job %+v", j)
			}
			names[j.Name] = true
		}
	})
}

func FuzzParseNeeds(f *testing.F) {
	seed(f, "needs.json")
	lim := DefaultLimits()
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := ParseNeeds(data, lim)
		if err == nil && len(n) > lim.MaxNeedsFlows {
			t.Fatalf("accepted %d workflows", len(n))
		}
	})
}
