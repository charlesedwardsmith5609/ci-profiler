// Package load reads saved GitHub Actions JSON from a directory. Input is untrusted: every
// parser returns an error on bad data and none of them panics.
package load

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
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
	ID          int64           `json:"id"`
	RunID       int64           `json:"run_id"`
	Attempt     int             `json:"run_attempt"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Conclusion  string          `json:"conclusion"`
	CreatedAt   ts              `json:"created_at"`
	StartedAt   ts              `json:"started_at"`
	CompletedAt ts              `json:"completed_at"`
	Steps       json.RawMessage `json:"steps"`
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

// expectDelim reads one token and requires it to be the given delimiter.
func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("expected %q, got %v", rune(want), tok)
	}
	return nil
}

// eachElement calls fn for each element of the JSON array at the decoder's position, stopping
// with an error as soon as more than max elements appear, so memory stays bounded. A JSON null
// counts as an empty array.
func eachElement(dec *json.Decoder, max int, what string, fn func() error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("%s must be an array", what)
	}
	n := 0
	for dec.More() {
		if n++; n > max {
			return fmt.Errorf("more than %d %s", max, what)
		}
		if err := fn(); err != nil {
			return err
		}
	}
	return expectDelim(dec, ']')
}

func parseSteps(raw json.RawMessage, lim Limits, job string) ([]model.Step, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var steps []model.Step
	dec := json.NewDecoder(bytes.NewReader(raw))
	err := eachElement(dec, lim.MaxSteps, "steps in job "+strconv.Quote(job), func() error {
		var s stepJSON
		if err := dec.Decode(&s); err != nil {
			return err
		}
		steps = append(steps, model.Step{
			Name: s.Name, Number: s.Number, Conclusion: s.Conclusion,
			StartedAt: s.StartedAt.Time, CompletedAt: s.CompletedAt.Time,
		})
		return nil
	})
	return steps, err
}

// ParseJobs parses one jobs API response. All jobs must share one run id and attempt, and job
// names must be unique. An empty jobs array is an error: it carries no run id to link on.
// Jobs and steps are decoded one at a time so the limits apply before memory is spent.
func ParseJobs(data []byte, lim Limits) ([]model.Job, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	var jobs []model.Job
	names := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if key != "jobs" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		jobs, names = nil, map[string]bool{}
		err = eachElement(dec, lim.MaxJobs, "jobs", func() error {
			var j jobJSON
			if err := dec.Decode(&j); err != nil {
				return err
			}
			if j.RunID <= 0 || j.Attempt < 1 {
				return fmt.Errorf("job %q: run_id and run_attempt are required", j.Name)
			}
			if len(jobs) > 0 && (j.RunID != jobs[0].RunID || j.Attempt != jobs[0].Attempt) {
				return fmt.Errorf("job %q: run_id/run_attempt differ from the first job", j.Name)
			}
			if names[j.Name] {
				return fmt.Errorf("duplicate job name %q in one attempt", j.Name)
			}
			names[j.Name] = true
			steps, err := parseSteps(j.Steps, lim, j.Name)
			if err != nil {
				return err
			}
			jobs = append(jobs, model.Job{
				ID: j.ID, RunID: j.RunID, Attempt: j.Attempt, Name: j.Name, Status: j.Status,
				Conclusion: j.Conclusion, CreatedAt: j.CreatedAt.Time, StartedAt: j.StartedAt.Time,
				CompletedAt: j.CompletedAt.Time, Steps: steps,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	if len(jobs) == 0 {
		return nil, errors.New("jobs array is empty, so it cannot be linked to a run; omit the file to record a run with no jobs")
	}
	return jobs, nil
}

// ParseNeeds parses needs.json: workflow path -> job name -> dependency names. References are
// not checked here; the analyzer validates them. Entries are counted as they are decoded.
func ParseNeeds(data []byte, lim Limits) (map[string]map[string][]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string][]string{}
	if tok != nil {
		if d, ok := tok.(json.Delim); !ok || d != '{' {
			return nil, errors.New("needs.json must be an object")
		}
		deps := 0
		for dec.More() {
			wfTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			wf := wfTok.(string) // object keys are always strings
			if len(out) >= lim.MaxNeedsFlows {
				return nil, fmt.Errorf("more than %d workflows", lim.MaxNeedsFlows)
			}
			jobsTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			out[wf] = map[string][]string{}
			if jobsTok == nil {
				continue
			}
			if d, ok := jobsTok.(json.Delim); !ok || d != '{' {
				return nil, fmt.Errorf("workflow %q must be an object of jobs", wf)
			}
			for dec.More() {
				jTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				job := jTok.(string)
				if len(out[wf]) >= lim.MaxNeedsJobs {
					return nil, fmt.Errorf("workflow %q: more than %d jobs", wf, lim.MaxNeedsJobs)
				}
				list := []string{}
				err = eachElement(dec, lim.MaxNeedsDeps, "dependencies", func() error {
					if deps++; deps > lim.MaxNeedsDeps {
						return fmt.Errorf("more than %d dependencies in needs.json", lim.MaxNeedsDeps)
					}
					var d string
					if err := dec.Decode(&d); err != nil {
						return err
					}
					list = append(list, d)
					return nil
				})
				if err != nil {
					return nil, err
				}
				out[wf][job] = list
			}
			if err := expectDelim(dec, '}'); err != nil {
				return nil, err
			}
		}
		if err := expectDelim(dec, '}'); err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return out, nil
}
