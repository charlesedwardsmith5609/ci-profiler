package analyze

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func secs(n ...int) []time.Duration {
	var out []time.Duration
	for _, v := range n {
		out = append(out, time.Duration(v)*time.Second)
	}
	return out
}

func TestPercentile(t *testing.T) {
	seq := func(n int) []time.Duration {
		var d []time.Duration
		for i := 1; i <= n; i++ {
			d = append(d, time.Duration(i)*time.Second)
		}
		return d
	}
	tests := []struct {
		name string
		in   []time.Duration
		p    int
		want time.Duration
	}{
		{"empty", nil, 95, 0},
		{"one p50", secs(7), 50, 7 * time.Second},
		{"one p95", secs(7), 95, 7 * time.Second},
		{"two p50", secs(1, 9), 50, 1 * time.Second},
		{"two p95", secs(1, 9), 95, 9 * time.Second},
		{"20 p50", seq(20), 50, 10 * time.Second},
		{"20 p95", seq(20), 95, 19 * time.Second},
		{"100 p95", seq(100), 95, 95 * time.Second},
		{"101 p95", seq(101), 95, 96 * time.Second},
		{"p0 is first", seq(5), 0, 1 * time.Second},
		{"p100 is last", seq(5), 100, 5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Percentile(tc.in, tc.p); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	in := secs(5, 1, 3)
	s := Summarize(in)
	if s.N != 3 || s.P50 != 3*time.Second || s.P95 != 5*time.Second || s.Total != 9*time.Second {
		t.Errorf("%+v", s)
	}
	if in[0] != 5*time.Second {
		t.Error("Summarize sorted the caller's slice")
	}
	big := []time.Duration{math.MaxInt64 - 1, 10}
	if s := Summarize(big); !s.Overflow || s.Total != math.MaxInt64 {
		t.Errorf("overflow not flagged: %+v", s)
	}
}

func TestSpan(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		start, end time.Time
		ok         bool
	}{
		{"normal", t0, t0.Add(time.Minute), true},
		{"zero length", t0, t0, true},
		{"zero start", time.Time{}, t0, false},
		{"zero end", t0, time.Time{}, false},
		{"reversed", t0.Add(time.Hour), t0, false},
		{"just under bound", t0, t0.Add(maxDuration), true},
		{"over bound", t0, t0.Add(maxDuration + time.Second), false},
		{"year 1 to 9999", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := span(tc.start, tc.end); ok != tc.ok {
				t.Errorf("ok = %v", ok)
			}
		})
	}
}

func nodes(spec ...any) []Node { // name, seconds, name, seconds...
	var out []Node
	for i := 0; i < len(spec); i += 2 {
		out = append(out, Node{spec[i].(string), time.Duration(spec[i+1].(int)) * time.Second})
	}
	return out
}

func TestLongestPath(t *testing.T) {
	tests := []struct {
		name     string
		nodes    []Node
		needs    map[string][]string
		want     string
		wantDur  time.Duration
		wantKind string
		wantText string
	}{
		{"single", nodes("a", 5), nil, "a", 5 * time.Second, "", ""},
		{"independent picks longest", nodes("a", 5, "b", 9), nil, "b", 9 * time.Second, "", ""},
		{"chain", nodes("a", 1, "b", 2, "c", 3), map[string][]string{"b": {"a"}, "c": {"b"}}, "a b c", 6 * time.Second, "", ""},
		{"diamond", nodes("a", 1, "b", 10, "c", 2, "d", 1), map[string][]string{"b": {"a"}, "c": {"a"}, "d": {"b", "c"}}, "a b d", 12 * time.Second, "", ""},
		{"tie breaks by name", nodes("x", 5, "y", 5), nil, "x", 5 * time.Second, "", ""},
		{"matrix expansion", nodes("build (linux)", 100, "build (mac)", 200, "test", 10), map[string][]string{"test": {"build"}}, "build (mac) test", 210 * time.Second, "", ""},
		{"matrix job uses base needs", nodes("lint", 5, "t (a)", 1, "t (b)", 9), map[string][]string{"t": {"lint"}}, "lint t (b)", 14 * time.Second, "", ""},
		{"unknown dependency", nodes("a", 1), map[string][]string{"a": {"ghost"}}, "", 0, KindUnknownDep, "ghost"},
		{"self dependency", nodes("a", 1), map[string][]string{"a": {"a"}}, "", 0, KindUnknownDep, "itself"},
		{"2-cycle", nodes("a", 1, "b", 1), map[string][]string{"a": {"b"}, "b": {"a"}}, "", 0, KindCycle, "a -> b -> a"},
		{"3-cycle", nodes("a", 1, "b", 1, "c", 1), map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}, "", 0, KindCycle, "a -> b -> c -> a"},
		{"matrix self loop", nodes("m (1)", 1), map[string][]string{"m": {"m"}}, "", 0, KindCycle, "m (1)"},
		{"cycle behind valid branch", nodes("ok", 1, "x", 1, "y", 1, "z", 1), map[string][]string{"z": {"ok", "x"}, "x": {"y"}, "y": {"x"}}, "", 0, KindCycle, "x -> y -> x"},
		{"two-node chain", nodes("a", 1, "b", 1), map[string][]string{"b": {"a"}}, "a b", 2 * time.Second, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, dur, err := runWithTimeout(t, tc.nodes, tc.needs)
			if tc.wantKind != "" {
				ge, ok := err.(*GraphError)
				if !ok || ge.Kind != tc.wantKind || !strings.Contains(ge.Detail, tc.wantText) {
					t.Fatalf("err = %v, want %s containing %q", err, tc.wantKind, tc.wantText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(path, " "); got != tc.want || dur != tc.wantDur {
				t.Errorf("path %q dur %v; want %q %v", got, dur, tc.want, tc.wantDur)
			}
		})
	}
}

func TestLongestPathOverflow(t *testing.T) {
	n := []Node{{"a", math.MaxInt64 - 5}, {"b", 100}}
	_, _, err := LongestPath(n, map[string][]string{"b": {"a"}}, 100)
	if ge, ok := err.(*GraphError); !ok || ge.Kind != KindOverflow {
		t.Errorf("err = %v", err)
	}
}

// runWithTimeout runs LongestPath in a goroutine and fails instead of hanging if it loops.
func runWithTimeout(t *testing.T, n []Node, needs map[string][]string) ([]string, time.Duration, error) {
	t.Helper()
	type out struct {
		path []string
		dur  time.Duration
		err  error
	}
	ch := make(chan out, 1) // buffered: the goroutine can always finish
	go func() {
		p, d, e := LongestPath(n, needs, 1_000_000)
		ch <- out{p, d, e}
	}()
	select {
	case o := <-ch:
		return o.path, o.dur, o.err
	case <-time.After(5 * time.Second):
		t.Fatal("LongestPath did not return: looped on the graph")
		return nil, 0, nil
	}
}

func TestLongestPathLargeGraphs(t *testing.T) {
	t.Run("chain of 5000", func(t *testing.T) {
		var ns []Node
		needs := map[string][]string{}
		for i := 0; i < 5000; i++ {
			ns = append(ns, Node{fmt.Sprintf("j%04d", i), time.Second})
			if i > 0 {
				needs[fmt.Sprintf("j%04d", i)] = []string{fmt.Sprintf("j%04d", i-1)}
			}
		}
		path, dur, err := runWithTimeout(t, ns, needs)
		if err != nil || len(path) != 5000 || dur != 5000*time.Second {
			t.Errorf("len %d dur %v err %v", len(path), dur, err)
		}
	})
	t.Run("fan-in of 4000", func(t *testing.T) {
		var ns []Node
		var all []string
		for i := 0; i < 4000; i++ {
			name := fmt.Sprintf("j%04d", i)
			ns = append(ns, Node{name, time.Second})
			all = append(all, name)
		}
		ns = append(ns, Node{"sink", time.Second})
		_, dur, err := runWithTimeout(t, ns, map[string][]string{"sink": all})
		if err != nil || dur != 2*time.Second {
			t.Errorf("dur %v err %v", dur, err)
		}
	})
	t.Run("matrix amplification is capped", func(t *testing.T) {
		var ns []Node
		for i := 0; i < 2000; i++ {
			ns = append(ns, Node{fmt.Sprintf("a (%d)", i), time.Second}, Node{fmt.Sprintf("b (%d)", i), time.Second})
		}
		_, _, err := runWithTimeout(t, ns, map[string][]string{"b": {"a"}}) // 2000 x 2000 = 4M edges
		if ge, ok := err.(*GraphError); !ok || ge.Kind != KindTooLarge {
			t.Errorf("err = %v", err)
		}
	})
}

func FuzzLongestPath(f *testing.F) {
	f.Add([]byte{3, 0, 1, 1, 2})
	f.Add([]byte{4, 1, 0, 0, 1})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		n := int(data[0])%12 + 1
		var ns []Node
		for i := 0; i < n; i++ {
			ns = append(ns, Node{fmt.Sprintf("j%d", i), time.Duration(i+1) * time.Second})
		}
		needs := map[string][]string{}
		for i := 1; i+1 < len(data); i += 2 {
			a, b := fmt.Sprintf("j%d", int(data[i])%n), fmt.Sprintf("j%d", int(data[i+1])%n)
			needs[a] = append(needs[a], b)
		}
		path, dur, err := LongestPath(ns, needs, 10_000)
		if err != nil {
			return
		}
		var max time.Duration
		for _, nd := range ns {
			if nd.Dur > max {
				max = nd.Dur
			}
		}
		if dur < max || len(path) == 0 {
			t.Fatalf("dur %v below longest node %v, path %v", dur, max, path)
		}
	})
}
