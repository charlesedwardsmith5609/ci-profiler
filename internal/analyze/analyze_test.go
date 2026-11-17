package analyze

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"ci-profiler/internal/load"
	"ci-profiler/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// jb describes a job by offsets (seconds) from the run's created time.
type jb struct {
	name             string
	start, end       int
	concl            string
	steps            [][3]any // name, start, end (offsets)
	created          int
	createdSet       bool
	rawStart, rawEnd *time.Time
}

func job(name string, start, end int) jb {
	return jb{name: name, start: start, end: end, concl: "success"}
}
func skipped(name string) jb { return jb{name: name, concl: "skipped"} }

type runSpec struct {
	id      int64
	attempt int
	created time.Time
	status  string
	concl   string
	noJobs  bool
	jobs    []jb
}

func at(c time.Time, s int) time.Time { return c.Add(time.Duration(s) * time.Second) }

func build(specs ...runSpec) *model.Dataset {
	ds := &model.Dataset{}
	for _, s := range specs {
		if s.status == "" {
			s.status = "completed"
		}
		if s.concl == "" {
			s.concl = "success"
		}
		if s.attempt == 0 {
			s.attempt = 1
		}
		a := model.Attempt{
			Run: model.Run{ID: s.id, Attempt: s.attempt, WorkflowID: 1, Name: "CI", Path: "ci.yml",
				Status: s.status, Conclusion: s.concl, CreatedAt: s.created},
			HasJobs: !s.noJobs,
		}
		for i, j := range s.jobs {
			mj := model.Job{ID: int64(i + 1), RunID: s.id, Attempt: s.attempt, Name: j.name, Conclusion: j.concl,
				CreatedAt: at(s.created, 0)}
			if j.createdSet {
				mj.CreatedAt = at(s.created, j.created)
			}
			if j.concl != "skipped" {
				mj.StartedAt, mj.CompletedAt = at(s.created, j.start), at(s.created, j.end)
			}
			if j.rawStart != nil {
				mj.StartedAt = *j.rawStart
			}
			if j.rawEnd != nil {
				mj.CompletedAt = *j.rawEnd
			}
			for _, st := range j.steps {
				mj.Steps = append(mj.Steps, model.Step{Name: st[0].(string), StartedAt: at(s.created, st[1].(int)), CompletedAt: at(s.created, st[2].(int))})
			}
			a.Jobs = append(a.Jobs, mj)
		}
		ds.Attempts = append(ds.Attempts, a)
	}
	return ds
}

func one(t *testing.T, ds *model.Dataset, o Options) (Workflow, *Result) {
	t.Helper()
	r := Analyze(ds, o)
	if len(r.Workflows) != 1 {
		t.Fatalf("workflows = %d", len(r.Workflows))
	}
	return r.Workflows[0], r
}

func TestWindowSelectionAndExclusions(t *testing.T) {
	good := func(id int64, hour int) runSpec {
		return runSpec{id: id, created: t0.Add(time.Duration(hour) * time.Hour), jobs: []jb{job("a", 5, 65)}}
	}
	ds := build(
		good(1, 1), good(2, 2),
		runSpec{id: 3, created: t0.Add(3 * time.Hour), status: "in_progress", jobs: []jb{job("a", 5, 65)}},
		runSpec{id: 4, created: t0.Add(4 * time.Hour), concl: "cancelled", jobs: []jb{job("a", 5, 65)}},
		runSpec{id: 5, created: t0.Add(5 * time.Hour), noJobs: true},
		runSpec{id: 6, created: t0.Add(6 * time.Hour), jobs: []jb{skipped("a")}},
		runSpec{id: 7, created: t0.Add(7 * time.Hour), jobs: []jb{{name: "a", concl: "success", rawStart: &time.Time{}, rawEnd: &time.Time{}}}}, // zero timestamps
	)
	w, _ := one(t, ds, Options{Window: 6})
	// Window = runs 7..2; run 1 is outside. Incomplete runs are NOT backfilled by older runs.
	if w.Window.Runs != 6 || w.Window.Timed != 1 {
		t.Fatalf("window %+v", w.Window)
	}
	want := Excluded{Incomplete: 1, Cancelled: 1, NoJobs: 1, NoRunJobs: 1, BadTimestamps: 1}
	if w.Window.Excluded != want {
		t.Errorf("excluded %+v, want %+v", w.Window.Excluded, want)
	}
	if w.RunTime.N != 1 || w.RunTime.P95 != 60*time.Second {
		t.Errorf("run time %+v", w.RunTime)
	}
}

func TestWindowTieBreakAndCap(t *testing.T) {
	r := func(id int64) runSpec { return runSpec{id: id, created: t0, jobs: []jb{job("a", 0, int(id))}} }
	w, _ := one(t, build(r(1), r(2), r(3), r(4)), Options{Window: 2})
	// Same created time: higher run ids win, so the sample is runs 4 and 3.
	if w.RunTime.N != 2 || w.RunTime.P50 != 3*time.Second || w.RunTime.P95 != 4*time.Second {
		t.Errorf("%+v", w.RunTime)
	}
}

func TestWorkflowTimesAndQueue(t *testing.T) {
	ds := build(runSpec{id: 1, created: t0, jobs: []jb{job("a", 10, 70), job("b", 30, 130)}})
	w, _ := one(t, ds, Options{})
	if w.RunTime.P50 != 120*time.Second {
		t.Errorf("wall = %v, want 120s", w.RunTime.P50)
	}
	if w.QueueTime.P50 != 10*time.Second {
		t.Errorf("queue = %v, want 10s", w.QueueTime.P50)
	}
}

func TestNegativeQueueFromClockSkewIsCountedNotUsed(t *testing.T) {
	early := t0.Add(-30 * time.Second)
	j := job("a", 0, 60)
	j.rawStart = &early // started 30s before the run was created
	end := t0.Add(60 * time.Second)
	j.rawEnd = &end
	w, _ := one(t, build(runSpec{id: 1, created: t0, jobs: []jb{j}}), Options{})
	if w.NegativeQueue != 1 || w.QueueTime.N != 0 || w.Jobs[0].NegativeQueue != 1 || w.Jobs[0].Queue.N != 0 {
		t.Errorf("neg=%d queue=%+v job=%+v", w.NegativeQueue, w.QueueTime, w.Jobs[0])
	}
	if w.RunTime.N != 1 { // run time itself is still valid
		t.Errorf("run time dropped: %+v", w.RunTime)
	}
}

func TestSharesAndSkippedJobs(t *testing.T) {
	ds := build(runSpec{id: 1, created: t0, jobs: []jb{job("a", 0, 30), job("b", 0, 90), skipped("c")}})
	w, _ := one(t, ds, Options{})
	if len(w.Jobs) != 2 {
		t.Fatalf("jobs %+v", w.Jobs)
	}
	if math.Abs(w.Jobs[0].Share-0.25) > 1e-9 || math.Abs(w.Jobs[1].Share-0.75) > 1e-9 {
		t.Errorf("shares %v %v", w.Jobs[0].Share, w.Jobs[1].Share)
	}
}

func TestBadTimestampFixtures(t *testing.T) {
	y1 := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	y9999 := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	huge := job("a", 0, 0)
	huge.rawStart, huge.rawEnd = &y1, &y9999
	reversed := job("a", 100, 50)
	for name, j := range map[string]jb{"year 1 to 9999": huge, "end before start": reversed} {
		t.Run(name, func(t *testing.T) {
			w, _ := one(t, build(runSpec{id: 1, created: t0, jobs: []jb{j}}), Options{})
			if w.Window.Excluded.BadTimestamps != 1 || w.Window.Timed != 0 || w.RunTime.N != 0 {
				t.Errorf("%+v", w.Window)
			}
			if len(w.Jobs) != 0 {
				t.Errorf("bad job leaked into stats: %+v", w.Jobs)
			}
		})
	}
}

func TestNoTimedRunsHasNoNaN(t *testing.T) {
	w, _ := one(t, build(runSpec{id: 1, created: t0, jobs: []jb{skipped("a")}}), Options{})
	if w.CriticalPath.Available || len(w.Jobs) != 0 || w.RunTime.N != 0 {
		t.Errorf("%+v", w)
	}
}

func TestWaste(t *testing.T) {
	// Run 1: attempt 1 fails (10 min), attempt 2 passes (2 min). Run 2: cancelled attempt 1
	// (3 min). Run 3: attempt 1 ok, attempt 2 cancelled (4 min) which is a rerun, not cancelled.
	m := func(n int) []jb { return []jb{job("a", 0, n*60)} }
	ds := build(
		runSpec{id: 1, attempt: 1, created: t0, concl: "failure", jobs: m(10)},
		runSpec{id: 1, attempt: 2, created: t0.Add(time.Hour), jobs: m(2)},
		runSpec{id: 2, attempt: 1, created: t0.Add(2 * time.Hour), concl: "cancelled", jobs: m(3)},
		runSpec{id: 3, attempt: 1, created: t0.Add(3 * time.Hour), jobs: m(5)},
		runSpec{id: 3, attempt: 2, created: t0.Add(4 * time.Hour), concl: "cancelled", jobs: m(4)},
	)
	w, _ := one(t, ds, Options{})
	got := w.Waste
	if got.Rerun != 6*time.Minute { // attempt 2 of run 1 (2) + attempt 2 of run 3 (4)
		t.Errorf("rerun = %v", got.Rerun)
	}
	if got.Cancelled != 3*time.Minute { // only run 2; the cancelled rerun is not double counted
		t.Errorf("cancelled = %v", got.Cancelled)
	}
	if got.Discarded != 15*time.Minute { // run 1 attempt 1 (10) + run 3 attempt 1 (5), informational
		t.Errorf("discarded = %v", got.Discarded)
	}
	if got.Attempts != 5 {
		t.Errorf("attempts = %d", got.Attempts)
	}
}

func needsDS(needs map[string][]string, jobs ...jb) *model.Dataset {
	ds := build(runSpec{id: 1, created: t0, jobs: jobs})
	if needs != nil {
		ds.Needs = map[string]map[string][]string{"ci.yml": needs}
	}
	return ds
}

func TestCriticalPathNoSidecarIsNotAnError(t *testing.T) {
	w, r := one(t, needsDS(nil, job("a", 0, 10)), Options{})
	if w.CriticalPath.Available || !strings.Contains(w.CriticalPath.Reason, "no needs data") || len(r.Errors) != 0 {
		t.Errorf("%+v errors=%v", w.CriticalPath, r.Errors)
	}
}

func TestCriticalPathWorks(t *testing.T) {
	ds := needsDS(map[string][]string{"test": {"build"}, "deploy": {"test"}},
		job("lint", 0, 20), job("build (linux)", 0, 100), job("build (mac)", 0, 150), job("test", 150, 250), job("deploy", 250, 300))
	w, r := one(t, ds, Options{})
	cp := w.CriticalPath
	if !cp.Available || strings.Join(cp.Jobs, "|") != "build (mac)|test|deploy" || cp.Duration != 300*time.Second {
		t.Errorf("%+v", cp)
	}
	if cp.OnPathRuns["test"] != 1 || cp.Runs != 1 || len(r.Errors) != 0 {
		t.Errorf("%+v %v", cp, r.Errors)
	}
}

func TestCycleIsAnErrorAndAnalysisContinues(t *testing.T) {
	ds := needsDS(map[string][]string{"a": {"b"}, "b": {"a"}}, job("a", 0, 10), job("b", 0, 10))
	done := make(chan *Result, 1)
	go func() { done <- Analyze(ds, Options{}) }()
	var r *Result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Analyze looped on a needs cycle")
	}
	if len(r.Errors) != 1 || r.Errors[0].Kind != KindCycle || !strings.Contains(r.Errors[0].Detail, "a -> b -> a") {
		t.Fatalf("errors = %+v", r.Errors)
	}
	w := r.Workflows[0]
	if w.CriticalPath.Available || w.RunTime.N != 1 || len(w.Jobs) != 2 {
		t.Errorf("other metrics should still be computed: %+v", w)
	}
}

func TestUnknownDependencyIsAnError(t *testing.T) {
	_, r := one(t, needsDS(map[string][]string{"a": {"ghost"}}, job("a", 0, 10)), Options{})
	if len(r.Errors) != 1 || r.Errors[0].Kind != KindUnknownDep {
		t.Errorf("%+v", r.Errors)
	}
}

func TestSkippedDependencyStillResolves(t *testing.T) {
	ds := needsDS(map[string][]string{"b": {"a"}}, skipped("a"), job("b", 0, 10))
	w, r := one(t, ds, Options{})
	if len(r.Errors) != 0 || !w.CriticalPath.Available || w.CriticalPath.Duration != 10*time.Second {
		t.Errorf("%+v %v", w.CriticalPath, r.Errors)
	}
}

func TestDominantStepFlag(t *testing.T) {
	steps := func(a, b int) [][3]any { return [][3]any{{"small", 0, a}, {"big", a, a + b}} }
	t.Run("one big step flags", func(t *testing.T) {
		j := job("build", 0, 100)
		j.steps = steps(10, 90)
		w, _ := one(t, needsDS(map[string][]string{}, j), Options{})
		if len(w.Flags) != 1 || !strings.Contains(w.Flags[0].Subject, "big") || w.Flags[0].Kind != "dominant-step" {
			t.Errorf("flags %+v", w.Flags)
		}
	})
	t.Run("long chain of equal jobs does not flag", func(t *testing.T) {
		var jobs []jb
		needs := map[string][]string{}
		for i := 0; i < 100; i++ {
			j := job(fmt.Sprintf("j%03d", i), i*60, i*60+60)
			j.steps = [][3]any{{"only", i * 60, i*60 + 60}}
			jobs = append(jobs, j)
			if i > 0 {
				needs[j.name] = []string{fmt.Sprintf("j%03d", i-1)}
			}
		}
		w, _ := one(t, needsDS(needs, jobs...), Options{})
		if len(w.Flags) != 0 {
			t.Errorf("%d flags for 1%% steps", len(w.Flags))
		}
	})
	t.Run("steps off the path are not flagged", func(t *testing.T) {
		big := job("side", 0, 20)
		big.steps = [][3]any{{"huge", 0, 20}}
		w, _ := one(t, needsDS(map[string][]string{}, big, job("main", 0, 100)), Options{})
		if len(w.Flags) != 0 {
			t.Errorf("flags %+v", w.Flags)
		}
	})
}

func TestStepsRankedAndTrimmed(t *testing.T) {
	j := job("a", 0, 100)
	j.steps = [][3]any{{"s1", 0, 10}, {"s2", 10, 60}, {"s3", 60, 100}}
	w, _ := one(t, build(runSpec{id: 1, created: t0, jobs: []jb{j}}), Options{TopSteps: 2})
	if len(w.Steps) != 2 || w.Steps[0].Name != "s2" || w.Steps[1].Name != "s3" {
		t.Errorf("%+v", w.Steps)
	}
}

func TestWorkflowsAreSeparatedAndSorted(t *testing.T) {
	ds := build(runSpec{id: 1, created: t0, jobs: []jb{job("a", 0, 1)}}, runSpec{id: 2, created: t0, jobs: []jb{job("a", 0, 1)}})
	ds.Attempts[1].Run.WorkflowID = 0
	ds.Attempts[1].Run.Path = "other.yml"
	r := Analyze(ds, Options{})
	if len(r.Workflows) != 2 || r.Workflows[0].Key != "id:1" || r.Workflows[1].Key != "path:other.yml" {
		t.Errorf("%+v", r.Workflows)
	}
}

func TestAnalyzeDoesNotModifyInput(t *testing.T) {
	ds := build(runSpec{id: 2, attempt: 2, created: t0, jobs: []jb{job("b", 0, 5), job("a", 0, 9)}},
		runSpec{id: 2, attempt: 1, created: t0, jobs: []jb{job("a", 0, 9)}})
	before := fmt.Sprintf("%+v", ds)
	Analyze(ds, Options{})
	if after := fmt.Sprintf("%+v", ds); after != before {
		t.Error("input changed")
	}
}

func dump(r *Result) string {
	var b strings.Builder
	d := func(s Summary) string { return fmt.Sprintf("n=%d p50=%s p95=%s total=%s", s.N, s.P50, s.P95, s.Total) }
	for _, w := range r.Workflows {
		fmt.Fprintf(&b, "workflow %s %q path=%s\n", w.Key, w.Name, w.Path)
		fmt.Fprintf(&b, "  window %+v\n  run   [%s] %s\n  queue [%s] %s\n", w.Window, LabelWorkflow, d(w.RunTime), LabelWfQueue, d(w.QueueTime))
		for _, j := range w.Jobs {
			fmt.Fprintf(&b, "  job %-14s run %s | queue %s | share %.4f\n", j.Name, d(j.Run), d(j.Queue), j.Share)
		}
		cp := w.CriticalPath
		fmt.Fprintf(&b, "  critical path available=%v jobs=%v dur=%s runs=%d reason=%q\n", cp.Available, cp.Jobs, cp.Duration, cp.Runs, cp.Reason)
		var keys []string
		for k := range cp.OnPathRuns {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "    on path %s: %d\n", k, cp.OnPathRuns[k])
		}
		for _, s := range w.Steps {
			fmt.Fprintf(&b, "  step %s / %s %s\n", s.Job, s.Name, d(s.Run))
		}
		fmt.Fprintf(&b, "  waste rerun=%s cancelled=%s discarded(info)=%s attempts=%d\n", w.Waste.Rerun, w.Waste.Cancelled, w.Waste.Discarded, w.Waste.Attempts)
		for _, f := range w.Flags {
			fmt.Fprintf(&b, "  flag %s %s: %s\n", f.Kind, f.Subject, f.Detail)
		}
	}
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "error %s %s: %s\n", e.Workflow, e.Kind, e.Detail)
	}
	return b.String()
}

func TestAnalyzeSampleGolden(t *testing.T) {
	ds, err := load.Load("../../testdata/sample", load.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	got := dump(Analyze(ds, Options{}))
	golden := "../../testdata/golden/analyze-sample.txt"
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
