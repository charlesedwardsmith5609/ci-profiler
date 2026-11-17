# Step 2: parser and data model (Phase 2)

Implements DESIGN.md sections 1, 7 and the loading half of 2. Standard library only, no go.mod
change. No CLI yet (Phase 4). No analysis yet (Phase 3).

## Files touched (new)
- `internal/model/model.go`: types below.
- `internal/load/limits.go`: `Limits` and `DefaultLimits()` (10 MiB/file, 200 MiB total, 5,000
  files, 5,000 jobs/file, 1,000 steps/job, needs: 1,000 workflows, 10,000 jobs/workflow, 100,000
  deps total, 20 reported errors).
- `internal/load/parse.go`: `ParseRun([]byte)`, `ParseJobs([]byte)`, `ParseNeeds([]byte)`.
- `internal/load/load.go`: `Load(dir string, lim Limits) (*model.Dataset, error)`.
- `internal/load/*_test.go`, `testdata/golden/load-sample.txt`.

## Types (model)
- `Run{ID int64; Attempt int; Name, Path, Event, Status, Conclusion string; CreatedAt, StartedAt,
  UpdatedAt time.Time; File string}`
- `Step{Name string; Number int; Conclusion string; StartedAt, CompletedAt time.Time}`
- `Job{ID, RunID int64; Attempt int; Name, Status, Conclusion string; CreatedAt, StartedAt,
  CompletedAt time.Time; Steps []Step}`. JSON null or absent timestamp = zero `time.Time`;
  judging a zero value as missing is Phase 3's job, and it is counted there, not dropped here.
- `Attempt{Run Run; Jobs []Job; HasJobs bool}`
- `Dataset{Attempts []Attempt; Needs map[string]map[string][]string}`; Attempts sorted by
  (run id, attempt) so output is deterministic.

## Behavior
- Layout per DESIGN.md section 1. Only `*.json` regular files in `runs/` and `jobs/` are read;
  other regular files are ignored; any non-regular entry (symlink, FIFO, dir, device) is an error
  naming the path. A missing `runs/` is an error; missing `jobs/` or `needs.json` is allowed.
- Every open goes through `os.OpenRoot(dir)` with `O_NONBLOCK`; `Stat` on the opened handle must
  be regular. Bytes read through `io.LimitReader(max+1)`; exceeding the per-file cap, the running
  total cap, or the file-count cap is an error. File count is checked from the directory listing
  before any file is read.
- Validation: run id > 0, attempt >= 1; all jobs in a file share run_id and run_attempt; duplicate
  (run, attempt) pairs across runs files or across jobs files are errors naming both files;
  duplicate job names in an attempt are errors; jobs file with no matching run is an error; a run
  with no jobs file gets `HasJobs=false`.
- Decoding uses `encoding/json` into structs, unknown fields ignored, trailing data after the
  value is an error. Malformed input never panics.
- Errors are `*FileError{Path, Err}`; `Load` collects up to 20 and returns them with
  `errors.Join`. Stage order: list and cap-check, read and parse, cross-file validation.
- Deferred to Phase 3 on purpose: needs reference checks, cycle detection, matrix expansion,
  timestamp sanity (missing or negative durations).

## Ownership
No goroutines or shared mutable state in production code. `Load` returns a `*Dataset` the caller
owns; nothing retains it. The one goroutine is in the FIFO test, owned by that test, which waits
on a channel with a 5 s timeout.

## Tests
- Table tests: ParseRun, ParseJobs, ParseNeeds with valid, null timestamps, wrong types, empty,
  trailing garbage, huge job and step counts, bad run ids.
- Load on `testdata/sample` compared with a golden summary (`-update` flag rewrites it).
- Temp-dir tests with real files: oversized file, too many files, total cap, symlink pointing
  outside root, symlink inside root, FIFO (`syscall.Mkfifo`, no writer; must return an error
  within the timeout instead of hanging), directory named `x.json`, duplicate pairs, mismatched
  run_id, duplicate job names, missing runs dir, empty runs dir.
- Fuzz: `FuzzParseRun`, `FuzzParseJobs`, `FuzzParseNeeds`, seeded from the sample; assert no panic
  and, on success, the validation invariants. Run for 30 s each once and report; seeds run in
  check.sh.
- Check: `bash scripts/check.sh` (gofmt, vet, govulncheck, `go test -race -count=1`).

## Revisions after plan review (self-approved, DECISIONS.md D12-D15)
- Directory reads use `File.ReadDir(100)` batches and stop as soon as the entry count (every
  entry, including ignored and non-regular ones, across both dirs) exceeds the file cap.
- Each read is bounded by `min(per-file cap, remaining total) + 1`; exceeding either stops the
  load immediately, and error collection never reads further files after a cap error.
- `Run` gains `WorkflowID int64`. Grouping key `Run.WorkflowKey()`: `id:<workflow_id>` when
  non-zero, else `path:<path>`; both empty is an input error. Needs lookup uses `Run.Path`.
- A jobs file with an empty `jobs` array is an input error ("cannot link to a run; omit the file
  to record a run with no jobs"). Linkage is never inferred from file names. Tested.
