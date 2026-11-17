// Package load reads saved GitHub Actions JSON from a directory. Input is untrusted: every
// parser returns an error on bad data and none of them panics.
package load

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"ci-profiler/internal/model"
)

// FileError ties an error to the input file it came from.
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// ts decodes an RFC 3339 timestamp. JSON null and absent values stay zero.
type ts struct{ time.Time }

func (t *ts) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("timestamp must be a string or null")
	}
	if s == "" {
		return nil
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("bad timestamp %q", s)
	}
	t.Time = v.UTC()
	return nil
}

type runJSON struct {
	ID         int64  `json:"id"`
	Attempt    int    `json:"run_attempt"`
	WorkflowID int64  `json:"workflow_id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	CreatedAt  ts     `json:"created_at"`
	StartedAt  ts     `json:"run_started_at"`
	UpdatedAt  ts     `json:"updated_at"`
}

type stepJSON struct {
	Name        string `json:"name"`
	Number      int    `json:"number"`
	Conclusion  string `json:"conclusion"`
	StartedAt   ts     `json:"started_at"`
	CompletedAt ts     `json:"completed_at"`
}

type jobJSON struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	Attempt     int        `json:"run_attempt"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	CreatedAt   ts         `json:"created_at"`
	StartedAt   ts         `json:"started_at"`
	CompletedAt ts         `json:"completed_at"`
	Steps       []stepJSON `json:"steps"`
}

// decode parses exactly one JSON value and rejects trailing data.
func decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// ParseRun parses one runs API object and checks its identity fields.
func ParseRun(data []byte) (model.Run, error) {
	var r runJSON
	if err := decode(data, &r); err != nil {
		return model.Run{}, err
	}
	if r.ID <= 0 {
		return model.Run{}, errors.New("run id must be a positive number")
	}
	if r.Attempt < 1 {
		return model.Run{}, errors.New("run_attempt must be 1 or more")
	}
	run := model.Run{
		ID: r.ID, Attempt: r.Attempt, WorkflowID: r.WorkflowID, Name: r.Name, Path: r.Path,
		Event: r.Event, Status: r.Status, Conclusion: r.Conclusion,
		CreatedAt: r.CreatedAt.Time, StartedAt: r.StartedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if run.WorkflowKey() == "" {
		return model.Run{}, errors.New("run needs workflow_id or path to identify its workflow")
	}
	return run, nil
}

// ParseJobs parses one jobs API response. All jobs must share one run id and attempt, and job
// names must be unique. An empty jobs array is an error: it carries no run id to link on.
func ParseJobs(data []byte, lim Limits) ([]model.Job, error) {
	var resp struct {
		Jobs []jobJSON `json:"jobs"`
	}
	if err := decode(data, &resp); err != nil {
		return nil, err
	}
	if len(resp.Jobs) == 0 {
		return nil, errors.New("jobs array is empty, so it cannot be linked to a run; omit the file to record a run with no jobs")
	}
	if len(resp.Jobs) > lim.MaxJobs {
		return nil, fmt.Errorf("%d jobs exceeds the limit of %d", len(resp.Jobs), lim.MaxJobs)
	}
	jobs := make([]model.Job, 0, len(resp.Jobs))
	names := make(map[string]bool, len(resp.Jobs))
	for i, j := range resp.Jobs {
		if j.RunID <= 0 || j.Attempt < 1 {
			return nil, fmt.Errorf("job %d: run_id and run_attempt are required", i)
		}
		if j.RunID != resp.Jobs[0].RunID || j.Attempt != resp.Jobs[0].Attempt {
			return nil, fmt.Errorf("job %q: run_id/run_attempt differ from the first job", j.Name)
		}
		if names[j.Name] {
			return nil, fmt.Errorf("duplicate job name %q in one attempt", j.Name)
		}
		names[j.Name] = true
		if len(j.Steps) > lim.MaxSteps {
			return nil, fmt.Errorf("job %q: %d steps exceeds the limit of %d", j.Name, len(j.Steps), lim.MaxSteps)
		}
		job := model.Job{
			ID: j.ID, RunID: j.RunID, Attempt: j.Attempt, Name: j.Name, Status: j.Status,
			Conclusion: j.Conclusion, CreatedAt: j.CreatedAt.Time, StartedAt: j.StartedAt.Time,
			CompletedAt: j.CompletedAt.Time,
		}
		for _, s := range j.Steps {
			job.Steps = append(job.Steps, model.Step{
				Name: s.Name, Number: s.Number, Conclusion: s.Conclusion,
				StartedAt: s.StartedAt.Time, CompletedAt: s.CompletedAt.Time,
			})
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// ParseNeeds parses needs.json: workflow path -> job name -> dependency names. References are
// not checked here; the analyzer validates them.
func ParseNeeds(data []byte, lim Limits) (map[string]map[string][]string, error) {
	var n map[string]map[string][]string
	if err := decode(data, &n); err != nil {
		return nil, err
	}
	if len(n) > lim.MaxNeedsFlows {
		return nil, fmt.Errorf("%d workflows exceeds the limit of %d", len(n), lim.MaxNeedsFlows)
	}
	deps := 0
	for wf, jobs := range n {
		if len(jobs) > lim.MaxNeedsJobs {
			return nil, fmt.Errorf("workflow %q: %d jobs exceeds the limit of %d", wf, len(jobs), lim.MaxNeedsJobs)
		}
		for _, d := range jobs {
			deps += len(d)
			if deps > lim.MaxNeedsDeps {
				return nil, fmt.Errorf("more than %d dependencies in needs.json", lim.MaxNeedsDeps)
			}
		}
	}
	return n, nil
}
