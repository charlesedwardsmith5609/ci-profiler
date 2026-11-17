package load

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseRun(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"valid", `{"id":1,"run_attempt":1,"workflow_id":5,"created_at":"2026-01-01T00:00:00Z"}`, ""},
		{"path only", `{"id":1,"run_attempt":2,"path":".github/workflows/ci.yml"}`, ""},
		{"null timestamp", `{"id":1,"run_attempt":1,"workflow_id":5,"created_at":null}`, ""},
		{"empty", ``, "EOF"},
		{"not json", `hello`, "invalid"},
		{"array", `[]`, "cannot unmarshal"},
		{"trailing data", `{"id":1,"run_attempt":1,"workflow_id":5} {}`, "unexpected data"},
		{"zero id", `{"id":0,"run_attempt":1,"workflow_id":5}`, "positive"},
		{"negative id", `{"id":-3,"run_attempt":1,"workflow_id":5}`, "positive"},
		{"zero attempt", `{"id":1,"run_attempt":0,"workflow_id":5}`, "run_attempt"},
		{"id wrong type", `{"id":"x","run_attempt":1}`, "cannot unmarshal"},
		{"id overflow", `{"id":99999999999999999999,"run_attempt":1}`, "cannot unmarshal"},
		{"bad timestamp", `{"id":1,"run_attempt":1,"workflow_id":5,"created_at":"yesterday"}`, "bad timestamp"},
		{"timestamp number", `{"id":1,"run_attempt":1,"workflow_id":5,"created_at":5}`, "timestamp"},
		{"no workflow identity", `{"id":1,"run_attempt":1}`, "workflow"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ParseRun([]byte(tc.in))
			checkErr(t, err, tc.wantErr)
			if err == nil && r.ID != 1 {
				t.Errorf("id = %d", r.ID)
			}
		})
	}
}

func TestParseRunTimestampsAreUTC(t *testing.T) {
	r, err := ParseRun([]byte(`{"id":1,"run_attempt":1,"workflow_id":5,"created_at":"2026-01-01T02:00:00+02:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !r.CreatedAt.Equal(want) || r.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v", r.CreatedAt)
	}
}

func TestWorkflowKey(t *testing.T) {
	a, _ := ParseRun([]byte(`{"id":1,"run_attempt":1,"workflow_id":5,"path":"x"}`))
	b, _ := ParseRun([]byte(`{"id":1,"run_attempt":1,"path":"x"}`))
	if a.WorkflowKey() != "id:5" || b.WorkflowKey() != "path:x" {
		t.Errorf("keys = %q %q", a.WorkflowKey(), b.WorkflowKey())
	}
}

func TestParseJobs(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxJobs, lim.MaxSteps = 3, 2
	j := func(id int, name string) string {
		return `{"id":` + string(rune('0'+id)) + `,"run_id":7,"run_attempt":1,"name":"` + name + `","started_at":null}`
	}
	tests := []struct {
		name    string
		in      string
		wantErr string
		wantN   int
	}{
		{"one job", `{"jobs":[` + j(1, "a") + `]}`, "", 1},
		{"null started", `{"jobs":[` + j(1, "a") + `]}`, "", 1},
		{"empty array", `{"jobs":[]}`, "empty", 0},
		{"missing jobs key", `{}`, "empty", 0},
		{"too many jobs", `{"jobs":[` + strings.Join([]string{j(1, "a"), j(2, "b"), j(3, "c"), j(4, "d")}, ",") + `]}`, "more than 3 jobs", 0},
		{"duplicate names", `{"jobs":[` + j(1, "a") + `,` + j(2, "a") + `]}`, "duplicate job name", 0},
		{"mixed run ids", `{"jobs":[` + j(1, "a") + `,{"id":2,"run_id":8,"run_attempt":1,"name":"b"}]}`, "differ", 0},
		{"mixed attempts", `{"jobs":[` + j(1, "a") + `,{"id":2,"run_id":7,"run_attempt":2,"name":"b"}]}`, "differ", 0},
		{"missing run id", `{"jobs":[{"id":1,"name":"a"}]}`, "required", 0},
		{"too many steps", `{"jobs":[{"id":1,"run_id":7,"run_attempt":1,"name":"a","steps":[{},{},{}]}]}`, "steps", 0},
		{"bad step time", `{"jobs":[{"id":1,"run_id":7,"run_attempt":1,"name":"a","steps":[{"started_at":"x"}]}]}`, "bad timestamp", 0},
		{"jobs wrong type", `{"jobs":{}}`, "must be an array", 0},
		{"truncated", `{"jobs":[{"id":1`, "unexpected EOF", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jobs, err := ParseJobs([]byte(tc.in), lim)
			checkErr(t, err, tc.wantErr)
			if err == nil && len(jobs) != tc.wantN {
				t.Errorf("jobs = %d, want %d", len(jobs), tc.wantN)
			}
		})
	}
}

func TestParseNeeds(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxNeedsFlows, lim.MaxNeedsJobs, lim.MaxNeedsDeps = 1, 2, 3
	tests := []struct {
		name, in, wantErr string
	}{
		{"valid", `{"ci.yml":{"a":[],"b":["a"]}}`, ""},
		{"empty object", `{}`, ""},
		{"too many workflows", `{"a":{},"b":{}}`, "more than 1 workflows"},
		{"too many jobs", `{"a":{"x":[],"y":[],"z":[]}}`, "more than 2 jobs"},
		{"too many deps", `{"a":{"x":["1","2"],"y":["3","4"]}}`, "dependencies"},
		{"wrong shape", `{"a":["x"]}`, "object of jobs"},
		{"null", `null`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseNeeds([]byte(tc.in), lim)
			checkErr(t, err, tc.wantErr)
		})
	}
}

func checkErr(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want != "" && err == nil:
		t.Fatalf("want error containing %q, got nil", want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

// allocated runs f and returns the bytes allocated while it ran.
func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// A file full of tiny entries must be rejected by count before memory is spent on all of them.
func TestParseLimitsBoundMemory(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxSteps, lim.MaxNeedsDeps = 3, 3
	const n = 1_000_000
	steps := `{"jobs":[{"id":1,"run_id":7,"run_attempt":1,"name":"a","steps":[` + strings.Repeat("{},", n) + `{}]}]}`
	deps := `{"w":{"j":[` + strings.Repeat(`"x",`, n) + `"x"]}}`
	for name, tc := range map[string]func() error{
		"steps": func() error { _, err := ParseJobs([]byte(steps), lim); return err },
		"deps":  func() error { _, err := ParseNeeds([]byte(deps), lim); return err },
	} {
		var err error
		got := allocated(func() { err = tc() })
		if err == nil || !strings.Contains(err.Error(), "more than 3") {
			t.Errorf("%s: err = %v", name, err)
		}
		if got > 32<<20 { // raw bytes are copied a few times; decoding all 1M entries costs 100+ MB
			t.Errorf("%s: allocated %d bytes", name, got)
		}
	}
}
