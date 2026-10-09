// Package metrics is a minimal Prometheus text-format registry: counters and
// gauges with labels, enough for an operator's dashboard and alerting.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type kind string

const (
	counter kind = "counter"
	gauge   kind = "gauge"
)

type family struct {
	help   string
	kind   kind
	values map[string]float64 // rendered label set -> value
}

type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

func New() *Registry {
	return &Registry{families: map[string]*family{}}
}

func (r *Registry) family(name, help string, k kind) *family {
	f, ok := r.families[name]
	if !ok {
		f = &family{help: help, kind: k, values: map[string]float64{}}
		r.families[name] = f
	}
	return f
}

// labelString renders key/value pairs as {k="v",...} in key order.
func labelString(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	if len(labels)%2 != 0 {
		panic("metrics: labels must be key/value pairs")
	}
	type pair struct{ k, v string }
	pairs := make([]pair, 0, len(labels)/2)
	for i := 0; i < len(labels); i += 2 {
		pairs = append(pairs, pair{labels[i], labels[i+1]})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(p.v)
		parts[i] = fmt.Sprintf(`%s="%s"`, p.k, v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Add increments a counter.
func (r *Registry) Add(name, help string, v float64, labels ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.family(name, help, counter).values[labelString(labels)] += v
}

// Inc increments a counter by one.
func (r *Registry) Inc(name, help string, labels ...string) {
	r.Add(name, help, 1, labels...)
}

// Set sets a gauge.
func (r *Registry) Set(name, help string, v float64, labels ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.family(name, help, gauge).values[labelString(labels)] = v
}

// Write renders all metrics in Prometheus text exposition format.
func (r *Registry) Write(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.families))
	for n := range r.families {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := r.families[n]
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", n, f.help, n, f.kind); err != nil {
			return err
		}
		labels := make([]string, 0, len(f.values))
		for l := range f.values {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		for _, l := range labels {
			if _, err := fmt.Fprintf(w, "%s%s %g\n", n, l, f.values[l]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Handler serves the registry at /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		r.Write(w)
	})
}
