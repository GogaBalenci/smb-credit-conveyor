// Package metrics — минимальный реестр счётчиков в формате Prometheus text exposition.
// Без внешних зависимостей; в проде заменяется на prometheus/client_golang
// без изменения имён метрик.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Registry хранит счётчики и gauge-функции.
type Registry struct {
	mu       sync.Mutex
	counters map[string]map[string]float64 // name -> labels -> value
	help     map[string]string
	gauges   []gauge
}

type gauge struct {
	name, help string
	fn         func() map[string]float64
}

// New создаёт реестр.
func New() *Registry {
	return &Registry{counters: map[string]map[string]float64{}, help: map[string]string{}}
}

// Default — глобальный реестр процесса.
var Default = New()

// Inc увеличивает счётчик name с метками вида "k1","v1","k2","v2".
func (r *Registry) Inc(name, help string, labels ...string) {
	r.Add(name, help, 1, labels...)
}

// Add увеличивает счётчик на delta.
func (r *Registry) Add(name, help string, delta float64, labels ...string) {
	key := labelString(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters[name] == nil {
		r.counters[name] = map[string]float64{}
		r.help[name] = help
	}
	r.counters[name][key] += delta
}

// Gauge регистрирует вычисляемую метрику (например, длину очереди из БД).
func (r *Registry) Gauge(name, help string, fn func() map[string]float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges = append(r.gauges, gauge{name, help, fn})
}

// Value возвращает значение счётчика (для тестов).
func (r *Registry) Value(name string, labels ...string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters[name][labelString(labels)]
}

// Write выводит метрики в текстовом формате Prometheus.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	names := make([]string, 0, len(r.counters))
	for n := range r.counters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", n, r.help[n], n)
		keys := make([]string, 0, len(r.counters[n]))
		for k := range r.counters[n] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s%s %g\n", n, k, r.counters[n][k])
		}
	}
	gauges := append([]gauge(nil), r.gauges...)
	r.mu.Unlock()

	for _, g := range gauges {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.name, g.help, g.name)
		vals := g.fn()
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s%s %g\n", g.name, k, vals[k])
		}
	}
}

// Label формирует строку меток для gauge-функций.
func Label(kv ...string) string { return labelString(kv) }

func labelString(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
		parts = append(parts, fmt.Sprintf(`%s="%s"`, kv[i], v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
