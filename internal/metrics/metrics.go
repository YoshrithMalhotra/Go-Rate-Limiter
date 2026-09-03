package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var durationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

type labels struct {
	Policy    string
	Algorithm string
	Outcome   string
}

type histogram struct {
	Buckets []uint64
	Count   uint64
	Sum     float64
}

type Registry struct {
	mu        sync.RWMutex
	checks    map[labels]uint64
	durations map[labels]*histogram
}

func NewRegistry() *Registry {
	return &Registry{
		checks:    make(map[labels]uint64),
		durations: make(map[labels]*histogram),
	}
}

func (r *Registry) ObserveCheck(policyName, algorithm, outcome string, duration time.Duration) {
	if r == nil {
		return
	}
	if policyName == "" {
		policyName = "inline"
	}
	if algorithm == "" {
		algorithm = "unknown"
	}
	if outcome == "" {
		outcome = "unknown"
	}

	key := labels{Policy: policyName, Algorithm: algorithm, Outcome: outcome}
	seconds := duration.Seconds()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.checks[key]++
	h := r.durations[key]
	if h == nil {
		h = &histogram{Buckets: make([]uint64, len(durationBuckets))}
		r.durations[key] = h
	}
	for i, bucket := range durationBuckets {
		if seconds <= bucket {
			h.Buckets[i]++
		}
	}
	h.Count++
	h.Sum += seconds
}

func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprint(w, r.Render())
	}
}

func (r *Registry) Render() string {
	if r == nil {
		return ""
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]labels, 0, len(r.checks))
	for key := range r.checks {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].String() < keys[j].String()
	})

	var b strings.Builder
	b.WriteString("# HELP governor_checks_total Total rate-limit checks by policy, algorithm, and outcome.\n")
	b.WriteString("# TYPE governor_checks_total counter\n")
	for _, key := range keys {
		fmt.Fprintf(&b, "governor_checks_total%s %d\n", key.promLabels(), r.checks[key])
	}

	b.WriteString("# HELP governor_check_duration_seconds Rate-limit check duration in seconds.\n")
	b.WriteString("# TYPE governor_check_duration_seconds histogram\n")
	for _, key := range keys {
		h := r.durations[key]
		if h == nil {
			continue
		}
		for i, bucket := range durationBuckets {
			fmt.Fprintf(&b, "governor_check_duration_seconds_bucket%s %d\n", key.promLabelsWith("le", fmt.Sprintf("%.3g", bucket)), h.Buckets[i])
		}
		fmt.Fprintf(&b, "governor_check_duration_seconds_bucket%s %d\n", key.promLabelsWith("le", "+Inf"), h.Count)
		fmt.Fprintf(&b, "governor_check_duration_seconds_sum%s %.9f\n", key.promLabels(), h.Sum)
		fmt.Fprintf(&b, "governor_check_duration_seconds_count%s %d\n", key.promLabels(), h.Count)
	}

	return b.String()
}

func (l labels) String() string {
	return l.Policy + "\xff" + l.Algorithm + "\xff" + l.Outcome
}

func (l labels) promLabels() string {
	return fmt.Sprintf(`{policy="%s",algorithm="%s",outcome="%s"}`,
		escapeLabel(l.Policy),
		escapeLabel(l.Algorithm),
		escapeLabel(l.Outcome),
	)
}

func (l labels) promLabelsWith(name, value string) string {
	return fmt.Sprintf(`{policy="%s",algorithm="%s",outcome="%s",%s="%s"}`,
		escapeLabel(l.Policy),
		escapeLabel(l.Algorithm),
		escapeLabel(l.Outcome),
		name,
		escapeLabel(value),
	)
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}
