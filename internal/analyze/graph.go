package analyze

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type frame struct{ node, next int }

// Node is one job in a run's graph.
type Node struct {
	Name string
	Dur  time.Duration
}

// Error kinds for GraphError and AnalysisError.
const (
	KindCycle      = "needs-cycle"
	KindUnknownDep = "needs-unknown-dependency"
	KindTooLarge   = "needs-too-large"
	KindOverflow   = "overflow"
)

// GraphError is a problem with a run's needs graph.
type GraphError struct{ Kind, Detail string }

func (e *GraphError) Error() string { return e.Kind + ": " + e.Detail }

// expand resolves a dependency name to node indexes: jobs named d or starting "d (".
func expand(names []string, d string) []int {
	var out []int
	for i, n := range names {
		if n == d || strings.HasPrefix(n, d+" (") {
			out = append(out, i)
		}
	}
	return out
}

// lookupDeps returns the declared dependencies for a job: exact name first, then the name
// before " (" for matrix jobs.
func lookupDeps(needs map[string][]string, name string) []string {
	if d, ok := needs[name]; ok {
		return d
	}
	if i := strings.Index(name, " ("); i > 0 {
		return needs[name[:i]]
	}
	return nil
}

// LongestPath finds the longest chain by node duration. needs maps a job name (or matrix base
// name) to dependency names. It returns the chain in execution order and its total duration.
// It is iterative, so deep graphs cannot exhaust the stack, and it reports cycles instead of
// looping. Errors are *GraphError: a cycle, an unknown dependency, a self dependency, too many edges, overflow.
func LongestPath(nodes []Node, needs map[string][]string, maxEdges int) ([]string, time.Duration, error) {
	n := len(nodes)
	if n == 0 {
		return nil, 0, nil
	}
	names := make([]string, n)
	for i, nd := range nodes {
		names[i] = nd.Name
	}
	deps := make([][]int, n)
	cache := map[string][]int{} // each distinct dependency name is resolved once
	edges := 0
	for i, nd := range nodes {
		for _, d := range lookupDeps(needs, nd.Name) {
			if d == nd.Name {
				return nil, 0, &GraphError{KindUnknownDep, fmt.Sprintf("job %q needs itself", nd.Name)}
			}
			targets, seen := cache[d]
			if !seen {
				targets = expand(names, d)
				cache[d] = targets
			}
			if len(targets) == 0 {
				return nil, 0, &GraphError{KindUnknownDep, fmt.Sprintf("job %q needs %q, which matches no job in this run", nd.Name, d)}
			}
			if edges += len(targets); edges > maxEdges {
				return nil, 0, &GraphError{KindTooLarge, fmt.Sprintf("more than %d dependency edges after matrix expansion", maxEdges)}
			}
			deps[i] = append(deps[i], targets...)
		}
	}

	// Iterative DFS, post-order: every node is finished after all of its dependencies.
	const (
		white = iota
		grey
		black
	)
	order := make([]int, 0, n)
	color := make([]int, n)
	byName := make([]int, n) // visit roots in name order so output is stable
	for i := range byName {
		byName[i] = i
	}
	sort.Slice(byName, func(a, b int) bool { return names[byName[a]] < names[byName[b]] })
	for _, root := range byName {
		if color[root] != white {
			continue
		}
		stack := []frame{{root, 0}}
		color[root] = grey
		for len(stack) > 0 {
			f := &stack[len(stack)-1]
			if f.next < len(deps[f.node]) {
				d := deps[f.node][f.next]
				f.next++
				switch color[d] {
				case white:
					color[d] = grey
					stack = append(stack, frame{d, 0})
				case grey:
					return nil, 0, &GraphError{KindCycle, strings.Join(cycleNames(stack, d, names), " -> ")}
				}
				continue
			}
			color[f.node] = black
			order = append(order, f.node)
			stack = stack[:len(stack)-1]
		}
	}

	// Longest path DP in dependency order. best[i] = nodes[i].Dur + max best[dep].
	best := make([]time.Duration, n)
	prev := make([]int, n)
	for _, i := range order {
		prev[i] = -1
		var top time.Duration
		for _, d := range deps[i] {
			if prev[i] == -1 || best[d] > top || (best[d] == top && names[d] < names[prev[i]]) {
				prev[i], top = d, best[d]
			}
		}
		var over bool
		if best[i], over = addDur(nodes[i].Dur, top); over {
			return nil, 0, &GraphError{KindOverflow, fmt.Sprintf("critical path duration overflows at job %q", nodes[i].Name)}
		}
	}
	end := 0
	for i := 1; i < n; i++ {
		if best[i] > best[end] || (best[i] == best[end] && names[i] < names[end]) {
			end = i
		}
	}
	var path []string
	for i := end; i != -1; i = prev[i] {
		path = append(path, names[i])
	}
	for l, r := 0, len(path)-1; l < r; l, r = l+1, r-1 {
		path[l], path[r] = path[r], path[l]
	}
	return path, best[end], nil
}

// cycleNames extracts the cycle closed by back-edge target from the DFS stack, as
// "a -> b -> a" in dependency direction (each job needs the next).
func cycleNames(stack []frame, target int, names []string) []string {
	start := 0
	for i, f := range stack {
		if f.node == target {
			start = i
			break
		}
	}
	var out []string
	for _, f := range stack[start:] {
		out = append(out, names[f.node])
	}
	return append(out, names[target])
}
