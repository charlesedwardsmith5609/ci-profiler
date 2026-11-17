# ci-profiler design

Offline, read-only, job-level timing profiler for saved GitHub Actions data. Standard library
only. Never calls an API, never handles a token, never executes input.

## 1. Input layout
```
<dir>/runs/*.json      one run object per file (runs API)
<dir>/jobs/*.json      one {"jobs":[...]} response per (run, attempt) (jobs API)
<dir>/needs.json       optional: {"<workflow path>": {"<job>": ["<dep>", ...]}}
--workflows <dir>      optional workflow YAML, used only for flag checks
```
- A run is identified by `(run.id, run.run_attempt)` read from file content, not file names.
- Each jobs file links to a run through its jobs' `run_id` and `run_attempt`. All jobs in one
  file must share both values, otherwise the file is an error.
- Two runs files with the same pair, or two jobs files for the same pair, are an error that
  names both files. A run with no jobs file is listed as "no jobs data" and excluded.

## 2. Needs graph
- Node identity: the job `name` as the API reports it. Matrix jobs appear as `build (linux, 1.22)`.
- Duplicate job names within one attempt are an error (two jobs both named `test` cannot be
  told apart by this tool), naming the name.
- A sidecar dependency `build` expands to every job named `build` or starting with `build (`.
- An unknown dependency, or a job that is its own dependency, is an error naming the job.
- A cycle is detected by DFS with colors (no recursion deeper than the node cap; node cap 10,000
  per run) and reported with the cycle's job names. The tool never loops on it.
- Without `needs.json` for a workflow, critical path is "unavailable (no needs data)". No path is
  inferred from timestamps, because overlap does not prove dependency.

## 3. Job metrics (every output number carries its formula label)
- queue = started_at - created_at
- run = completed_at - started_at
- Jobs with a missing or non-monotonic timestamp are excluded and counted in a "skipped" line.
- share = job run minutes / sum of all job run minutes in the window. Parallel jobs make the sum
  exceed wall time, and the label says so.
- Slowest steps: step completed_at - started_at, ranked within the window.
- Critical path: longest path by job run time over the needs DAG (topological DP). Queue time is
  shown beside it, not inside it. Ties break by job name for stable output.
- A step "dominates" when it is at least 50% of its critical-path job's run time (flag, threshold
  is a documented default, `--dominant-step-share`).

## 4. Workflow metrics and the p50/p95 sample
- Sample: for each workflow, the latest N runs (`--window`, default 50) ordered by
  run created_at descending, ties by run id. Only the final attempt of each run id is eligible.
  Incomplete runs and runs with missing timestamps are excluded and counted.
- Workflow run time = max(job completed_at) - min(job started_at) of that attempt (wall time).
- Workflow queue time = min(job started_at) - run created_at.
- p50/p95: nearest rank on the sample, rank = ceil(p/100 * n). The label states n.
- `--budget-p95` compares against workflow run time p95 (wall). Workflows with fewer than 1
  eligible run are reported and do not pass or fail silently: they print "no data".

## 5. Waste
- Each attempt is counted once. Minutes = sum of its jobs' run time.
- Rerun minutes: attempts with run_attempt > 1.
- Cancelled minutes: attempts with conclusion `cancelled`, labeled "cancelled (assumed
  superseded)" because the data cannot prove supersession. Not double counted with reruns.
- Informational only: minutes of earlier attempts that a later attempt replaced.

## 6. Caveats
- Clock skew: timestamps come from GitHub, but `started_at` is stamped when the runner picks the
  job up, so runner and service clocks can differ by seconds. Negative durations are excluded,
  never clamped silently.
- Queued vs started: queue includes approval waits, concurrency group waits and runner
  availability, so it is not purely capacity.
- Step times have one-second resolution; short steps are noisy.
- Job minutes are not billed minutes. Not a cost tool.
- Small windows give unstable p95. The label shows n.

## 7. Threat model
- Input is untrusted. Files opened through `os.Root` rooted at the input dir, so symlinks and
  `..` cannot escape even if the tree changes during the run.
- Caps: 10 MiB per file, 200 MiB total, 5,000 files, enforced with a limited reader on the opened
  stream, not file metadata. JSON nesting and step/job counts are capped.
- Only regular files are read. Each file is opened through the root with `O_NONBLOCK` (so a FIFO
  with no writer cannot hang the open), then `Stat` on the opened handle must report a regular
  file, otherwise it is an error. Directory walking skips nothing silently: a non-regular entry
  is an error naming the path. `O_NONBLOCK` is Unix-only; Windows support is a non-goal.
- Strict decoding, unknown fields ignored, errors name the file. No panics on bad input (fuzzed).
- No exec, no network, no writes except stdout/stderr.

## 8. Layout and ownership
- `cmd/ci-profiler`, `internal/model`, `internal/load`, `internal/analyze`, `internal/report`.
- No goroutines and no shared mutable state. The loader returns values the analyzer reads.
- Exit codes: 0 ok, 1 budget exceeded, 2 usage or input error.
- Tests: table tests with fixtures, golden files, fuzz on the parser, budget exit-code tests, a
  cycle test. Any wait has a timeout.
