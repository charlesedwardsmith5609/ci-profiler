package load

// Limits bounds every kind of input. Zero fields are invalid; use DefaultLimits.
type Limits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxFiles      int // entries across runs/ and jobs/, including ignored ones
	MaxJobs       int // per jobs file
	MaxSteps      int // per job
	MaxNeedsFlows int // workflows in needs.json
	MaxNeedsJobs  int // jobs per workflow in needs.json
	MaxNeedsDeps  int // dependency names in needs.json overall
	MaxErrors     int // file errors reported before giving up
}

// DefaultLimits returns the caps from docs/DESIGN.md.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:  10 << 20,
		MaxTotalBytes: 200 << 20,
		MaxFiles:      5000,
		MaxJobs:       5000,
		MaxSteps:      1000,
		MaxNeedsFlows: 1000,
		MaxNeedsJobs:  10000,
		MaxNeedsDeps:  100000,
		MaxErrors:     20,
	}
}
