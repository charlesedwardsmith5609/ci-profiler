// Package model holds the immutable data the loader produces and the analyzer reads.
package model

import (
	"strconv"
	"time"
)

// Run is one attempt of a workflow run, from the runs API.
type Run struct {
	ID         int64
	Attempt    int
	WorkflowID int64
	Name       string
	Path       string
	Event      string
	Status     string
	Conclusion string
	CreatedAt  time.Time
	StartedAt  time.Time
	UpdatedAt  time.Time
	File       string
}

// WorkflowKey groups runs of the same workflow: the workflow id when present, else the path.
// It returns "" when neither is known.
func (r Run) WorkflowKey() string {
	if r.WorkflowID != 0 {
		return "id:" + strconv.FormatInt(r.WorkflowID, 10)
	}
	if r.Path != "" {
		return "path:" + r.Path
	}
	return ""
}

// Step is one step of a job. A zero time means the API value was null or absent.
type Step struct {
	Name        string
	Number      int
	Conclusion  string
	StartedAt   time.Time
	CompletedAt time.Time
}

// Job is one job of one run attempt. A zero time means null or absent.
type Job struct {
	ID          int64
	RunID       int64
	Attempt     int
	Name        string
	Status      string
	Conclusion  string
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	Steps       []Step
}

// Attempt is a run attempt and its jobs. HasJobs is false when no jobs file was supplied.
type Attempt struct {
	Run     Run
	Jobs    []Job
	HasJobs bool
}

// Dataset is the loaded input. Attempts are sorted by (run id, attempt). Needs maps workflow
// path to job name to dependency names. The caller owns it; the loader keeps no reference.
type Dataset struct {
	Attempts []Attempt
	Needs    map[string]map[string][]string
}
