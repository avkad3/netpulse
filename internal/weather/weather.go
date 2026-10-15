// Package weather turns availability and latency statistics into an
// "internet weather" report: sunny, cloudy, rainy or stormy.
package weather

import "github.com/avkad3/netpulse/internal/store"

// Condition is a weather-style health rating.
type Condition string

const (
	Sunny   Condition = "sunny"   // healthy: fully available and fast
	Cloudy  Condition = "cloudy"  // degraded: slow or the odd failure
	Rainy   Condition = "rainy"   // poor: frequent failures or very slow
	Stormy  Condition = "stormy"  // outage: mostly failing
	Unknown Condition = "unknown" // not enough data yet
)

var rank = map[Condition]int{Unknown: 0, Sunny: 1, Cloudy: 2, Rainy: 3, Stormy: 4}

// Thresholds tune the classifier. Latency limits apply to p95.
type Thresholds struct {
	SunnyUptime, CloudyUptime, RainyUptime float64 // %
	SunnyP95Ms, CloudyP95Ms                float64
}

// Default thresholds suit probes to well-connected public services.
var Default = Thresholds{
	SunnyUptime: 99.5, CloudyUptime: 97, RainyUptime: 80,
	SunnyP95Ms: 250, CloudyP95Ms: 800,
}

// Report is the weather for one target.
type Report struct {
	Target    string    `json:"target"`
	Condition Condition `json:"condition"`
	Summary   string    `json:"summary"`
}

// Classify rates one target from its recent statistics.
func (t Thresholds) Classify(s store.Stats) Report {
	r := Report{Target: s.Target}
	switch {
	case s.Count == 0:
		r.Condition, r.Summary = Unknown, "waiting for data"
	case s.UptimePct < t.RainyUptime:
		r.Condition, r.Summary = Stormy, "outage: most probes failing"
	case s.UptimePct < t.CloudyUptime || s.P95Ms > t.CloudyP95Ms:
		r.Condition, r.Summary = Rainy, "unreliable or very slow"
	case s.UptimePct < t.SunnyUptime || s.P95Ms > t.SunnyP95Ms:
		r.Condition, r.Summary = Cloudy, "degraded: occasional failures or elevated latency"
	default:
		r.Condition, r.Summary = Sunny, "healthy"
	}
	return r
}

// Overall summarises many targets: one outage is a storm warning for
// that service, but the overall sky is the condition most targets share,
// worsened if a quarter or more are rainy/stormy.
func Overall(reports []Report) Condition {
	counts := map[Condition]int{}
	known := 0
	for _, r := range reports {
		if r.Condition != Unknown {
			counts[r.Condition]++
			known++
		}
	}
	if known == 0 {
		return Unknown
	}
	bad := counts[Rainy] + counts[Stormy]
	switch {
	case counts[Stormy]*2 >= known:
		return Stormy
	case bad*4 >= known:
		return Rainy
	case counts[Sunny]*2 > known:
		if bad > 0 || counts[Cloudy] > 0 {
			return Cloudy
		}
		return Sunny
	default:
		return Cloudy
	}
}

// Worse reports whether a is a worse condition than b.
func Worse(a, b Condition) bool { return rank[a] > rank[b] }
