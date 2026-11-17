# Decisions

- D1: Defaults from REQUIREMENTS.md "Confirmed decisions" accepted (window 50, caps, etc).
- D2: needs come from sidecar `needs.json`; no timing-inferred critical path. Plan review #1.
  Open for later: parsing `needs` from workflow YAML (needs a YAML-subset parser; not approved).
- D3: Waste counts each attempt once; rerun = attempt>1, plus cancelled; discarded earlier
  attempts informational. Plan review #2 (partly agreed: the reviewer's rerun predicate adopted,
  the discarded-attempt figure kept as info).
- D4: Link runs and jobs by (run_id, run_attempt) from content; duplicates error. Review #3.
- D5: Workflow run time is wall time; p50/p95 sample defined in DESIGN.md. Review #4.
- D6: Matrix dependency expansion by name prefix; unknown/ambiguous = error. Review #5.
- D7: Use `os.Root` and stream-level caps. Review #6.
- D8: Plan review ran with gitleaks missing (no secret scan); plan contained no secrets.
- D9: Cross-review #1: open files with O_NONBLOCK and require a regular file on the opened
  handle (FIFO hang). Agreed; Unix-only.
- D10: Cross-review #2: duplicate job names in an attempt are an error. Agreed.
- D11: Layout checked against a synthetic sample in testdata/sample (not real data). A real
  capture is still needed to confirm field shapes.
- D12: Bounded directory enumeration, ignored entries count toward the cap. Plan review (step 2).
- D13: Reads bounded by remaining total budget; stop on cap error.
- D14: Run keeps workflow_id; WorkflowKey falls back to path.
- D15: Empty jobs response is an input error; no filename-based linkage.
- D16: go.mod bumped 1.26.8 -> 1.26.9 (approved) for GO-2026-6604 (os.Root, Windows only, reachable via Root.OpenFile/Lstat). No module requirements added.
- D17: Cross-review (phase 2) #1: jobs, steps and needs decoded as streams with limits applied per element. Agreed.
- D18: #2: directories opened with O_DIRECTORY|O_NONBLOCK. Agreed; no replacement-race test (needs hooks).
- D19: #3: bytes actually read are always deducted from the total budget. Partly agreed: a per-file overrun stays a collected error, not fatal; the total cap stops the load.
- D20: #4: duplicate and unmatched-jobs branches honor MaxErrors. Agreed.
- D21: Checked duration arithmetic, 60-day single-duration validity bound. Plan review (step 3).
- D22: Cap expanded needs edges (1,000,000 per run).
- D23: Window = latest N distinct runs regardless of status; no backfill.
- D24: Structured Result.Errors separate from unavailable reasons (cycle is an error, missing sidecar is not).
- D25: Dominant step is path-relative, default 25% of critical path. Threshold is my choice; human may change.
- D26: All-skipped runs excluded; zero denominators guarded.
