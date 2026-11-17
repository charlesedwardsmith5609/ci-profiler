# ci-profiler: requirements (DRAFT, awaiting human confirmation)

Purpose: an offline, job-level timing profiler for GitHub Actions. It shows where pipeline
minutes go. Distinct from devex-metrics (DORA outcomes) and platform-templates (CI definitions).

## Input
- R1. Reads saved GitHub Actions runs/jobs API JSON files from a directory. Never calls an API,
  never reads or handles a token, never executes anything from the input.
- R2. Optional: workflow YAML files, supplied separately, used only for the "flag" checks.
- R2a. Job dependencies (`needs`) are not in the jobs API. They come from an optional sidecar
  `needs.json` per workflow. Without it the critical path is reported as unavailable, never guessed.
- R3. Input is untrusted. Caps on per-file size, total bytes, and file count. Symlinks are not
  followed outside the root. A malformed or oversized file gives a clear error, never a panic.

## Per-workflow metrics (each labeled with how it was computed)
- R4. Queue time (created_at to started_at) and run time.
- R5. Critical path through the `needs` graph, with caveats (clock skew, queued vs started).
  A `needs` cycle is reported as an error, not looped on.
- R6. Slowest steps.
- R7. Wasted minutes: reruns (run_attempt > 1) and cancelled-superseded runs.
- R8. Each job's share of total job minutes.
- R9. p50 and p95 over a window of runs.

## Output and gates
- R10. Formats: text table, JSON, CSV.
- R11. `--budget-p95 DURATION` exits non-zero when any workflow's p95 exceeds it.
- R12. `--diff old-dir new-dir` shows per-job change, suitable for a PR comment.

## Flags (report, never fix)
- R13. Jobs with no `timeout-minutes` (needs workflow file).
- R14. Workflows with no concurrency cancellation (needs workflow file).
- R15. Steps that dominate the critical path.

## Constraints
- R16. Go, standard library only. No new dependencies without approval.
- R17. Tests: table tests with fixture runs; golden output files; fuzz test on the JSON parser;
  caps tests; exit-code tests for the budget gate; cycle test. Every wait has a timeout.
- R18. No speedup number is claimed unless measured.

## Non-goals
- No runner cost in dollars. No data fetching. No judging test quality. Does not replace
  devex-metrics.

## Process (from the ask)
- Phases: 1 design doc and critical-path method; 2 parser and data model; 3 critical path and
  waste; 4 report formats and budget gate; 5 diff mode, CI example, README with sample report.
- Per CLAUDE.md: one plan file per step in `plans/step-N.md`, reviewed with
  `scripts/plan-review.sh`, approved by the human before code. Stop after each approved step.
- Decisions logged in `DECISIONS.md`. Cross-model review at the end of each phase.

## Confirmed decisions
- Window: last N runs per workflow, `--window N`, default 50.
- Critical path weight: job run time (started to completed); queue time reported separately.
- Caps: 10 MiB per file, 200 MiB total, 5,000 files.
- Cancelled runs are labeled "cancelled (assumed superseded)".
- Input layout: one JSON file per run (runs API object) plus one jobs-API response file per run,
  linked by run id. Exact layout is fixed in Phase 1 design and confirmed against a sample.
- Self-approval: fixes from plan and cross-model reviews that I agree with are self-approved and
  logged in DECISIONS.md. Work still stops at the end of each phase.
- Branch push: no git remote is configured; commit on a branch locally until one exists.
