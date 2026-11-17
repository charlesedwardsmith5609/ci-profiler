package analyze

// Formula labels attached to every derived number in reports.
const (
	LabelQueue      = "queue = started_at - created_at"
	LabelRun        = "run = completed_at - started_at"
	LabelWorkflow   = "workflow run = max(job completed_at) - min(job started_at), skipped jobs ignored"
	LabelWfQueue    = "workflow queue = min(job started_at) - run created_at"
	LabelPercentile = "nearest-rank percentile over the timed runs in the window"
	LabelShare      = "share = job run time / sum of all jobs' run time (parallel jobs make the sum exceed wall time)"
	LabelCritical   = "critical path = longest chain of job run times through the needs graph (queue time excluded)"
	LabelRerun      = "rerun minutes = job run time of attempts with run_attempt > 1"
	LabelCancelled  = "cancelled (assumed superseded) minutes = job run time of attempt-1 runs concluded cancelled"
	LabelDiscarded  = "discarded minutes (informational, not added) = job run time of attempts replaced by a later attempt"
	LabelDominant   = "dominant step = step duration >= share of the latest run's critical path duration"
)
