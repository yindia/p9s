package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registration binds an engine name to its factory.
type Registration struct {
	Name    string
	Factory Factory
}

// Registry holds the engines the binary knows about.
type Registry struct {
	mu   sync.RWMutex
	regs map[string]Registration
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{regs: map[string]Registration{}}
}

// Default is the process-wide registry engines add themselves to from init().
var Default = NewRegistry()

// Register adds an engine factory to the default registry. It panics on a
// duplicate name, which is a programming error.
func Register(name string, f Factory) {
	if err := Default.Register(name, f); err != nil {
		panic(err)
	}
}

// Register adds an engine factory.
func (r *Registry) Register(name string, f Factory) error {
	if name == "" || f == nil {
		return fmt.Errorf("register engine: name and factory are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.regs[name]; dup {
		return fmt.Errorf("register engine %q: already registered", name)
	}
	r.regs[name] = Registration{Name: name, Factory: f}
	return nil
}

// Names returns registered engine names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.regs))
	for n := range r.regs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Selection chooses which registered engines to consider. "auto" or "" means
// all of them; otherwise a comma-separated list of names.
func (r *Registry) Selection(spec string) ([]Registration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "auto" {
		out := make([]Registration, 0, len(r.regs))
		for _, n := range r.namesLocked() {
			out = append(out, r.regs[n])
		}
		return out, nil
	}
	var out []Registration
	seen := map[string]bool{}
	for _, n := range strings.Split(spec, ",") {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		reg, ok := r.regs[n]
		if !ok {
			return nil, fmt.Errorf("unknown engine %q (known: %s)", n, strings.Join(r.namesLocked(), ", "))
		}
		seen[n] = true
		out = append(out, reg)
	}
	return out, nil
}

func (r *Registry) namesLocked() []string {
	names := make([]string, 0, len(r.regs))
	for n := range r.regs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Detection is the outcome of running Detect for one engine.
type Detection struct {
	Engine    Engine
	Installed bool
	Version   string
	Err       error
}

// Detect builds every selected engine against the cluster and runs Detect
// concurrently. Results keep the registry's (sorted) order.
func Detect(ctx context.Context, regs []Registration, c Cluster) []Detection {
	out := make([]Detection, len(regs))
	var wg sync.WaitGroup
	for i, reg := range regs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := reg.Factory(c)
			installed, version, err := e.Detect(ctx)
			out[i] = Detection{Engine: e, Installed: installed, Version: version, Err: err}
		}()
	}
	wg.Wait()
	return out
}
