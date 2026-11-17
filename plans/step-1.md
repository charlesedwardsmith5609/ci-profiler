# Step 1: design doc and critical-path method (Phase 1)

No Go code in this step.

## Files touched
- `docs/DESIGN.md` (new): architecture, input layout, metric definitions, caveats, threat model.
- `DECISIONS.md` (new): D1.. log of decisions.
- No `go.mod` change. No tests (documentation only); check.sh still run.

## Content of DESIGN.md
1. Input layout: `<dir>/runs/*.json` (run object: id, name, workflow_id, path, run_attempt,
   status, conclusion, created_at, run_started_at, updated_at, event, head_sha) and
   `<dir>/jobs/*.json` (`{"jobs":[...]}`: id, run_id, name, status, conclusion, created_at,
   started_at, completed_at, steps[{name,number,started_at,completed_at,conclusion}]). Linked by
   run id. Optional `--workflows <dir>` of workflow YAML.
2. Needs graph: the jobs API does NOT include `needs`. Decision: needs come from the workflow
   YAML (needs a parser; stdlib has no YAML) or from an optional `needs` field in a small
   sidecar. Design proposal: sidecar `needs.json` per workflow (`{"job name":["dep",...]}`),
   and the critical path without it falls back to a timing-derived path (jobs ordered by
   completion/start overlap), clearly labeled "inferred". Minimal YAML subset parsing for
   `needs`/`timeout-minutes`/`concurrency` is a Phase 3 question, raised here, not decided.
3. Metric definitions, each with a formula string used in output labels:
   queue = started_at - created_at (job); run = completed_at - started_at;
   critical path = longest path by run time in needs DAG (topological DP, cycle -> error naming
   the cycle); waste = sum of job run time in attempts < latest attempt, plus cancelled runs;
   share = job minutes / total job minutes; p50/p95 = nearest-rank over window.
4. Caveats: clock skew between runner and API timestamps; queue time includes runner pick-up and
   approvals/concurrency waits, not only capacity; "started" is runner start not first step;
   matrix jobs are separate jobs; reruns reuse run id with new attempt, so jobs files must hold
   per-attempt data; parallel jobs make job-minute shares exceed wall time.
5. Threat model: size/file caps, `os.Root`-style no-escape symlink handling (Lstat, reject
   symlinks), strict JSON decode, no exec, no network, bounded recursion/graph size.
6. Package layout proposal: `internal/model`, `internal/load`, `internal/analyze`,
   `internal/report`, `cmd/ci-profiler`. Ownership: single-threaded, no shared mutable state;
   the loader returns immutable values. No goroutines planned.
7. Exit codes: 0 ok, 1 budget exceeded, 2 usage/input error.

## Tests
None this step. Verification: `bash scripts/check.sh` and review of the doc.

## Ownership
No shared state in this step.

## Revisions after plan review (self-approved, see DECISIONS.md D2-D7)
- Needs: sidecar `needs.json` only. No timing-inferred path; critical path is "unavailable" without it. (finding 1)
- Waste: each attempt counted once. Rerun minutes = job minutes of attempts with run_attempt>1;
  cancelled minutes = job minutes of cancelled attempts not already counted. Discarded earlier
  attempts reported as a separate informational line, not added to waste. (finding 2, partial)
- Layout: linkage by (run_id, run_attempt) taken from file content; duplicates are errors;
  a run with no jobs file is listed and excluded. (finding 3)
- Workflow metrics and p50/p95 sample defined in DESIGN.md section 4. (finding 4)
- Node identity and matrix expansion defined; unknown/ambiguous references are errors. (finding 5)
- Rooted opens via `os.Root` (go.mod is Go 1.26.8), caps enforced on the opened stream. (finding 6)
