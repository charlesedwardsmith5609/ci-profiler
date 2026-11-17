package analyze

import (
	"fmt"
	"sort"
	"time"

	"ci-profiler/internal/model"
)

// Options tunes Analyze. Zero values fall back to DefaultOptions.
type Options struct {
	Window            int     // latest N runs per workflow
	TopSteps          int     // slowest steps kept per workflow
	DominantStepShare float64 // share of the critical path that makes a step "dominant"
	MaxEdges          int     // needs edges allowed per run after matrix expansion
}

// DefaultOptions are the documented defaults.
func DefaultOptions() Options {
	return Options{Window: 50, TopSteps: 10, DominantStepShare: 0.25, MaxEdges: 1_000_000}
}

func (o Options) withDefaults() Options {
	d := DefaultOptions()
	if o.Window <= 0 {
		o.Window = d.Window
	}
	if o.TopSteps <= 0 {
		o.TopSteps = d.TopSteps
	}
	if o.DominantStepShare <= 0 {
		o.DominantStepShare = d.DominantStepShare
	}
	if o.MaxEdges <= 0 {
		o.MaxEdges = d.MaxEdges
	}
	return o
}

// Excluded counts window runs left out of the timed sample, by reason.
type Excluded struct {
	Incomplete, Cancelled, NoJobs, NoRunJobs, BadTimestamps int
}

// Window describes the run sample behind a workflow's numbers.
type Window struct {
	Requested int // N asked for
	Runs      int // distinct runs in the window
	Timed     int // runs that made it into the timed sample
	Excluded  Excluded
}

// JobStat aggregates one job name over the timed runs.
type JobStat struct {
	Name          string
	Run, Queue    Summary
	Share         float64
	NegativeQueue int // queue values dropped because started_at < created_at (clock skew)
}

// StepStat aggregates one step of one job over the timed runs.
type StepStat struct {
	Job, Name string
	Run       Summary
}

// CriticalPath is the longest chain of the latest timed run, plus how often each job was on it.
type CriticalPath struct {
	Available  bool
	Reason     string // why it is unavailable (not an error when no needs data was supplied)
	Jobs       []string
	Duration   time.Duration
	Runs       int            // timed runs a path was computed for
	OnPathRuns map[string]int // job name -> runs where it was on the path
}

// Waste is the minutes spent on reruns and cancelled runs in the window. Each attempt is
// counted once. Discarded is informational and is not part of the waste.
type Waste struct {
	Rerun, Cancelled, Discarded time.Duration
	Attempts                    int
}

// Flag is something worth looking at. Flags are reported, never acted on.
type Flag struct{ Kind, Subject, Detail string }

// AnalysisError is a problem with the input that the caller must report (cycle, bad needs).
type AnalysisError struct{ Workflow, Kind, Detail string }

// Workflow holds every number for one workflow.
type Workflow struct {
	Key, Name, Path string
	Window          Window
	RunTime         Summary
	QueueTime       Summary
	NegativeQueue   int
	Jobs            []JobStat
	CriticalPath    CriticalPath
	Steps           []StepStat
	Waste           Waste
	Flags           []Flag
}

// Result is what Analyze returns.
type Result struct {
	Workflows []Workflow
	Errors    []AnalysisError
}

type runKey struct {
	wf string
	id int64
}

type jobRec struct {
	name    string
	skipped bool
	run     time.Duration
	queue   time.Duration
	hasQ    bool
	negQ    bool
	steps   []stepRec
}

type stepRec struct {
	name string
	dur  time.Duration
}

type timedRun struct {
	att  *model.Attempt
	jobs []jobRec
	wall time.Duration
	q    time.Duration
	hasQ bool
	negQ bool
}

// evalJobs validates a final attempt's jobs. ok is false when any non-skipped job has bad
// timestamps; allSkipped is true when no job actually ran.
func evalJobs(a *model.Attempt) (recs []jobRec, ok, allSkipped bool) {
	allSkipped = true
	for _, j := range a.Jobs {
		if j.Conclusion == "skipped" {
			recs = append(recs, jobRec{name: j.Name, skipped: true})
			continue
		}
		allSkipped = false
		d, valid := span(j.StartedAt, j.CompletedAt)
		if !valid {
			return nil, false, false
		}
		r := jobRec{name: j.Name, run: d}
		if q, qok := span(j.CreatedAt, j.StartedAt); qok {
			r.queue, r.hasQ = q, true
		} else if !j.CreatedAt.IsZero() && j.StartedAt.Before(j.CreatedAt) {
			r.negQ = true
		}
		for _, s := range j.Steps {
			if sd, sok := span(s.StartedAt, s.CompletedAt); sok {
				r.steps = append(r.steps, stepRec{s.Name, sd})
			}
		}
		recs = append(recs, r)
	}
	return recs, true, allSkipped
}

// attemptMinutes sums valid, non-skipped job run times of one attempt.
func attemptMinutes(a *model.Attempt) (time.Duration, bool) {
	var total time.Duration
	var over bool
	for _, j := range a.Jobs {
		if j.Conclusion == "skipped" {
			continue
		}
		if d, ok := span(j.StartedAt, j.CompletedAt); ok {
			var o bool
			if total, o = addDur(total, d); o {
				over = true
			}
		}
	}
	return total, over
}

// Analyze computes per-workflow metrics. It never modifies ds.
func Analyze(ds *model.Dataset, opts Options) *Result {
	opts = opts.withDefaults()
	res := &Result{}

	groups := map[runKey][]*model.Attempt{}
	byWf := map[string][]runKey{}
	for i := range ds.Attempts {
		a := &ds.Attempts[i]
		k := runKey{a.Run.WorkflowKey(), a.Run.ID}
		if _, seen := groups[k]; !seen {
			byWf[k.wf] = append(byWf[k.wf], k)
		}
		groups[k] = append(groups[k], a)
	}
	wfKeys := make([]string, 0, len(byWf))
	for k := range byWf {
		wfKeys = append(wfKeys, k)
	}
	sort.Strings(wfKeys)

	for _, wk := range wfKeys {
		w, errs := analyzeWorkflow(ds, wk, byWf[wk], groups, opts)
		res.Workflows = append(res.Workflows, w)
		res.Errors = append(res.Errors, errs...)
	}
	return res
}

func analyzeWorkflow(ds *model.Dataset, wk string, keys []runKey, groups map[runKey][]*model.Attempt, opts Options) (Workflow, []AnalysisError) {
	var errs []AnalysisError
	addErr := func(kind, detail string) { errs = append(errs, AnalysisError{wk, kind, detail}) }

	type runInfo struct {
		final *model.Attempt
		all   []*model.Attempt
	}
	var runs []runInfo
	for _, k := range keys {
		all := groups[k]
		sort.Slice(all, func(i, j int) bool { return all[i].Run.Attempt < all[j].Run.Attempt })
		runs = append(runs, runInfo{all[len(all)-1], all})
	}
	// Latest first by the final attempt's creation time; ties by run id, newest id first.
	sort.Slice(runs, func(i, j int) bool {
		a, b := runs[i].final.Run, runs[j].final.Run
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	if len(runs) > opts.Window {
		runs = runs[:opts.Window]
	}

	w := Workflow{Key: wk, Name: runs[0].final.Run.Name, Path: runs[0].final.Run.Path}
	w.Window = Window{Requested: opts.Window, Runs: len(runs)}

	var timed []timedRun
	for _, r := range runs {
		f := r.final
		switch {
		case f.Run.Status != "completed":
			w.Window.Excluded.Incomplete++
			continue
		case f.Run.Conclusion == "cancelled":
			w.Window.Excluded.Cancelled++
			continue
		case !f.HasJobs:
			w.Window.Excluded.NoJobs++
			continue
		}
		recs, ok, allSkipped := evalJobs(f)
		if !ok {
			w.Window.Excluded.BadTimestamps++
			continue
		}
		if allSkipped {
			w.Window.Excluded.NoRunJobs++
			continue
		}
		var minStart, maxEnd time.Time
		for _, j := range f.Jobs {
			if j.Conclusion == "skipped" {
				continue
			}
			if minStart.IsZero() || j.StartedAt.Before(minStart) {
				minStart = j.StartedAt
			}
			if j.CompletedAt.After(maxEnd) {
				maxEnd = j.CompletedAt
			}
		}
		wall, wok := span(minStart, maxEnd)
		if !wok {
			w.Window.Excluded.BadTimestamps++
			continue
		}
		tr := timedRun{att: f, jobs: recs, wall: wall}
		if q, qok := span(f.Run.CreatedAt, minStart); qok {
			tr.q, tr.hasQ = q, true
		} else if !f.Run.CreatedAt.IsZero() && minStart.Before(f.Run.CreatedAt) {
			tr.negQ = true
		}
		timed = append(timed, tr)
	}
	w.Window.Timed = len(timed)

	// Workflow run and queue time.
	var walls, queues []time.Duration
	for _, t := range timed {
		walls = append(walls, t.wall)
		if t.hasQ {
			queues = append(queues, t.q)
		}
		if t.negQ {
			w.NegativeQueue++
		}
	}
	w.RunTime, w.QueueTime = Summarize(walls), Summarize(queues)
	if w.RunTime.Overflow || w.QueueTime.Overflow {
		addErr(KindOverflow, "workflow run or queue total overflows")
	}

	// Jobs and steps.
	runD, queueD := map[string][]time.Duration{}, map[string][]time.Duration{}
	negQ := map[string]int{}
	type stepKey struct{ job, step string }
	stepD := map[stepKey][]time.Duration{}
	for _, t := range timed {
		for _, j := range t.jobs {
			if j.skipped {
				continue
			}
			runD[j.name] = append(runD[j.name], j.run)
			if j.hasQ {
				queueD[j.name] = append(queueD[j.name], j.queue)
			}
			if j.negQ {
				negQ[j.name]++
			}
			for _, s := range j.steps {
				k := stepKey{j.name, s.name}
				stepD[k] = append(stepD[k], s.dur)
			}
		}
	}
	var grand time.Duration
	var grandOver bool
	for name, d := range runD {
		w.Jobs = append(w.Jobs, JobStat{Name: name, Run: Summarize(d), Queue: Summarize(queueD[name]), NegativeQueue: negQ[name]})
	}
	sort.Slice(w.Jobs, func(i, j int) bool { return w.Jobs[i].Name < w.Jobs[j].Name })
	for _, j := range w.Jobs {
		var o bool
		if grand, o = addDur(grand, j.Run.Total); o || j.Run.Overflow {
			grandOver = true
		}
	}
	if grandOver {
		addErr(KindOverflow, "job run time totals overflow; shares unavailable")
	} else if grand > 0 {
		for i := range w.Jobs {
			w.Jobs[i].Share = float64(w.Jobs[i].Run.Total) / float64(grand)
		}
	}
	for k, d := range stepD {
		w.Steps = append(w.Steps, StepStat{Job: k.job, Name: k.step, Run: Summarize(d)})
	}
	sort.Slice(w.Steps, func(i, j int) bool {
		a, b := w.Steps[i], w.Steps[j]
		if a.Run.P95 != b.Run.P95 {
			return a.Run.P95 > b.Run.P95
		}
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		return a.Name < b.Name
	})
	if len(w.Steps) > opts.TopSteps {
		w.Steps = w.Steps[:opts.TopSteps]
	}

	// Critical path.
	w.CriticalPath = criticalPaths(ds, &w, timed, opts, addErr)

	// Waste over every attempt of every window run.
	for _, r := range runs {
		for _, a := range r.all {
			m, over := attemptMinutes(a)
			if over {
				addErr(KindOverflow, fmt.Sprintf("attempt minutes overflow in run %d", a.Run.ID))
			}
			w.Waste.Attempts++
			var o bool
			switch {
			case a.Run.Attempt > 1:
				w.Waste.Rerun, o = addDur(w.Waste.Rerun, m)
			case a.Run.Conclusion == "cancelled":
				w.Waste.Cancelled, o = addDur(w.Waste.Cancelled, m)
			}
			if a != r.final {
				var o2 bool
				w.Waste.Discarded, o2 = addDur(w.Waste.Discarded, m)
				o = o || o2
			}
			if o {
				addErr(KindOverflow, "waste total overflows")
			}
		}
	}
	return w, errs
}

func criticalPaths(ds *model.Dataset, w *Workflow, timed []timedRun, opts Options, addErr func(kind, detail string)) CriticalPath {
	cp := CriticalPath{OnPathRuns: map[string]int{}}
	if len(timed) == 0 {
		cp.Reason = "no timed runs in the window"
		return cp
	}
	needs, ok := ds.Needs[w.Path]
	if !ok {
		cp.Reason = fmt.Sprintf("no needs data for %q (supply needs.json); path is never inferred from timestamps", w.Path)
		return cp
	}
	for i, t := range timed {
		nodes := make([]Node, 0, len(t.jobs))
		for _, j := range t.jobs {
			nodes = append(nodes, Node{j.name, j.run})
		}
		path, dur, err := LongestPath(nodes, needs, opts.MaxEdges)
		if err != nil {
			kind, detail := KindUnknownDep, err.Error()
			if ge, isGraph := err.(*GraphError); isGraph {
				kind, detail = ge.Kind, ge.Detail
			}
			detail = fmt.Sprintf("run %d: %s", t.att.Run.ID, detail)
			addErr(kind, detail)
			return CriticalPath{OnPathRuns: map[string]int{}, Reason: kind + ": " + detail}
		}
		cp.Runs++
		for _, n := range path {
			cp.OnPathRuns[n]++
		}
		if i == 0 {
			cp.Available, cp.Jobs, cp.Duration = true, path, dur
		}
	}
	// Dominant steps on the latest run's path.
	latest := timed[0]
	onPath := map[string]bool{}
	for _, n := range cp.Jobs {
		onPath[n] = true
	}
	for _, j := range latest.jobs {
		if !onPath[j.name] {
			continue
		}
		for _, s := range j.steps {
			if cp.Duration > 0 && float64(s.dur) >= opts.DominantStepShare*float64(cp.Duration) {
				w.Flags = append(w.Flags, Flag{"dominant-step", j.name + " / " + s.name,
					fmt.Sprintf("%s is %.0f%% of the %s critical path (%s)", s.dur, 100*float64(s.dur)/float64(cp.Duration), cp.Duration, LabelDominant)})
			}
		}
	}
	return cp
}
