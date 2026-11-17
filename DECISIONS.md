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
