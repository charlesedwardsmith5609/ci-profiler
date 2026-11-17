# Step 3: critical path and waste calculations (Phase 3)

Implements DESIGN.md sections 2 (graph), 3, 4 and 5. Pure functions over `*model.Dataset`.
Standard library only, no go.mod change. No CLI or output formats (Phase 4).

## Scope decisions
- In: job and workflow metrics, percentiles, critical path with cycle and reference errors,
  slowest steps, dominant-step flag, waste, shares.
- Out, deferred on purpose: the "no timeout" and "no concurrency cancellation" flags. They need
  workflow YAML, and the standard library cannot parse YAML. Proposal for Phase 4 prelude: a
  small line-based scanner for only `timeout-minutes` and `concurrency` (needs approval; named
  in Suggestions, not built here). Also out: any text/JSON/CSV rendering and budget logic.

## Files (new)
- `internal/analyze/stats.go`: `Percentile(sorted []time.Duration, p int)` nearest rank
  (rank = ceil(p/100*n)), `Summary{N; P50, P95, Total}`.
- `internal/analyze/graph.go`: `CriticalPath(jobs []Node, deps map[string][]string)`; iterative
  DFS (no recursion), cycle detection returning the cycle's names, longest path DP, ties by name.
- `internal/analyze/analyze.go`: `Analyze(ds, Options) *Result`; `Options{Window int (50),
  TopSteps int (10), DominantStepShare float64 (0.5)}`.
- `internal/analyze/labels.go`: formula strings reused by Phase 4 output.
- tests and `testdata/golden/analyze-sample.txt`.

## Result types
- `Result{Workflows []Workflow}` sorted by key.
- `Workflow{Key, Name, Path; Window{Requested, Runs, Timed int; Excluded Excluded};
  RunTime, QueueTime Summary; Jobs []JobStat; CriticalPath CriticalPath; Steps []StepStat;
  Waste Waste; Flags []Flag}`.
- `Excluded{Incomplete, Cancelled, NoJobs, BadTimestamps int}`: every excluded run is counted.
- `JobStat{Name; Run, Queue Summary; Share float64; SkippedJobs int}`.
- `CriticalPath{Available bool; Reason string; Jobs []string; Duration; OnPathRuns map[string]int;
  Runs int}`. Path shown is for the most recent timed run; `OnPathRuns` counts, per job, in how
  many timed runs it was on the path. Reason carries cycle text or the missing-needs message.
- `Waste{RerunMinutes, CancelledMinutes, DiscardedMinutes float64 (as Duration); Attempts int}`.
- `Flag{Kind, Subject, Detail}`; this step emits only `dominant-step`.

## Definitions
- Window runs: group attempts by (workflow key, run id); final attempt = highest run_attempt;
  window = latest N runs by final attempt `CreatedAt` desc, ties by run id desc; only runs whose
  final attempt `Status == completed` count as window runs (incomplete ones go to `Excluded`).
- Timed sample: window runs that are not cancelled, have jobs, and whose non-skipped jobs all have
  non-zero, ordered timestamps. A job with `conclusion == skipped` is a zero-length node and is
  never a timestamp error. Any other bad job excludes the whole run (counted `BadTimestamps`).
- Workflow run = max(completed) - min(started) over non-skipped jobs; queue = min(started) - run
  `CreatedAt`; a negative workflow queue (clock skew) is excluded from queue stats and counted.
- Job stats over timed runs' final attempts: run = completed - started, queue = started -
  created; negative job queue excluded from queue summary only. Share = job total run time / sum
  of all jobs' total run time (parallel jobs: sum exceeds wall time).
- Steps: duration = completed - started (both non-zero, ordered), aggregated by (job, step name).
- Needs: `ds.Needs[run.Path]`, key = exact job name, else the name before " (". A dependency `d`
  expands to jobs named `d` or starting `d (`. Jobs with no entry have no dependencies. An
  expansion that matches no job in the run, or a self-dependency, makes the workflow's critical
  path unavailable with the reason naming the job and run; a cycle does the same, naming the
  cycle. Analysis of everything else continues. No sidecar for the workflow path: unavailable
  ("no needs data"), never inferred.
- Critical path weight = job run time; queue is not on the path. A step dominates when its
  duration >= DominantStepShare x its job's run time and the job is on the latest run's path.
- Waste, over all attempts of window runs, each attempt once: attempt minutes = sum of its valid
  jobs' run time. Rerun = attempts with run_attempt > 1. Cancelled = attempts with conclusion
  `cancelled` that are attempt 1 (later attempts are already rerun). Discarded (informational,
  not added) = attempts below the final attempt.

## Ownership
No goroutines, no shared state. `Analyze` only reads the Dataset and returns a new `*Result` the
caller owns. The cycle test runs `Analyze` in one goroutine and waits on a channel with a 5 s
timeout.

## Tests
- Table: Percentile (n=1, n=2, 20, 100, p50/p95 boundaries, empty); timestamp validity; skipped
  jobs; shares sum to 1; matrix expansion; unknown dependency; self dependency; no sidecar.
- Cycles: 2-cycle, 3-cycle, self loop, cycle behind a valid branch: each returns within the
  timeout with the cycle named; large chain (5,000 nodes) and wide fan-in finish quickly.
- Waste: rerun, cancelled, cancelled rerun not double counted, discarded informational only.
- Window: more runs than N, ties, incomplete, cancelled, no-jobs counted in Excluded.
- Golden: `Analyze(testdata/sample)` rendered by a test-only dumper; `-update` flag.
- Fuzz `FuzzCriticalPath`: random node counts and edges from bytes; asserts it terminates and, on
  success, path duration >= max single node and path is a valid chain.
- Check: `bash scripts/check.sh`.

## Revisions after plan review (self-approved, DECISIONS.md D21-D26)
- Overflow (D21): a single job, step or run duration above 60 days is invalid (counted as bad
  timestamps; GitHub's own limit is shorter). All sums use checked addition; on overflow the metric
  is reported in `Result.Errors` kind `overflow` and omitted. Fixtures: year 0001-9999 timestamps,
  and a sum that overflows.
- Amplification (D22): `Options.MaxEdges` (default 1,000,000 per run). Expansion stops and errors
  `needs-too-large` before allocating beyond it. Test: two 2,000-node matrices with one dependency.
- Window (D23): the window is the latest N distinct runs by final-attempt CreatedAt regardless of
  status. Exclusions are classified inside that fixed window and never backfilled. Test it.
- Errors (D24): `Result.Errors []AnalysisError{Workflow, Kind, Detail}` for needs-cycle,
  needs-unknown-dependency, needs-too-large, overflow. "No needs data" is not an error: it is only
  `CriticalPath.Reason`. Other metrics continue after an error. Tests cover both.
- Dominance (D25): path-relative. A step dominates when its duration >= DominantStepShare x the
  latest run's critical path duration, for steps in jobs on that path. Default 0.25 (a documented
  default, flagged to the human). Tests: one 60% step flags; 100 one-minute jobs do not.
- All-skipped runs (D26): excluded (`Excluded.NoRunJobs`). Every ratio guards a zero denominator
  and yields 0 with `Available=false` where a share is meaningless; no NaN or Inf ever escapes.
