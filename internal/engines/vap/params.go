package vap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/enginekit"
	"github.com/yindia/p9s/internal/model"
)

// paramRef is a binding's reference to its policy's param object.
type paramRef struct {
	binding    model.ObjectRef
	apiVersion string
	kind       string
	namespace  string
	name       string
	// selector refs (paramRef.selector) match any number of objects: not
	// checked.
	selector bool
	// notFound is parameterNotFoundAction: Deny (default) or Allow.
	notFound string
}

func (r paramRef) key() string {
	return r.apiVersion + "|" + r.kind + "|" + r.namespace + "|" + r.name
}

// paramRefOf reads binding.spec.paramRef against policy.spec.paramKind.
func paramRefOf(policy, binding *unstructured.Unstructured) (paramRef, bool) {
	pr, ok, _ := unstructured.NestedMap(binding.Object, "spec", "paramRef")
	if !ok {
		return paramRef{}, false
	}
	pk, _, _ := unstructured.NestedMap(policy.Object, "spec", "paramKind")
	r := paramRef{
		binding: refOf(binding), apiVersion: enginekit.Str(pk, "apiVersion"), kind: enginekit.Str(pk, "kind"),
		namespace: enginekit.Str(pr, "namespace"), name: enginekit.Str(pr, "name"),
		notFound: enginekit.Str(pr, "parameterNotFoundAction"),
	}
	_, r.selector = pr["selector"]
	if r.notFound == "" {
		r.notFound = "Deny"
	}
	return r, true
}

// paramStatus is a checked reference.
type paramStatus struct {
	found bool
	// perRequest is set for a namespaced param kind with no namespace in
	// the ref: the API server reads it from the request's namespace, so
	// there is no single object to check.
	perRequest bool
	err        error // the check itself failed (RBAC, unknown kind)
	checked    time.Time
}

// recheckAfter bounds how stale a param check may be; the worker rechecks
// on this interval too, so a param created later clears its issue.
const recheckAfter = time.Minute

// pollIdle is how often waitIdle re-reads the in-memory queue.
const pollIdle = 20 * time.Millisecond

// paramChecker looks param objects up off the read path: reads never hit
// the API server, so want() only queues refs and a worker resolves them.
type paramChecker struct {
	cluster engine.Cluster
	mu      sync.Mutex
	results map[string]paramStatus
	pending []paramRef      // unbounded: nothing is dropped
	queued  map[string]bool // in pending or being checked
	last    []paramRef      // the latest refs, for timed rechecks
	wake    chan struct{}
	gen     atomic.Uint64
	notify  func()
}

func newParamChecker(c engine.Cluster) *paramChecker {
	return &paramChecker{cluster: c, results: map[string]paramStatus{}, queued: map[string]bool{}, wake: make(chan struct{}, 1)}
}

func (p *paramChecker) generation() uint64 { return p.gen.Load() }

func (p *paramChecker) start(ctx context.Context, notify func()) {
	p.mu.Lock()
	p.notify = notify
	p.mu.Unlock()
	go p.run(ctx)
}

func (p *paramChecker) run(ctx context.Context) {
	tick := time.NewTicker(recheckAfter)
	defer tick.Stop()
	for {
		r, ok := p.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
			case <-tick.C:
				p.mu.Lock()
				last := p.last
				p.mu.Unlock()
				p.want(last)
			}
			continue
		}
		st := p.check(ctx, r)
		if ctx.Err() != nil {
			return // stopped: a cancelled lookup is not a result
		}
		p.mu.Lock()
		p.results[r.key()] = st
		delete(p.queued, r.key())
		n := p.notify
		p.mu.Unlock()
		p.gen.Add(1)
		if n != nil {
			n()
		}
	}
}

func (p *paramChecker) next() (paramRef, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) == 0 {
		return paramRef{}, false
	}
	r := p.pending[0]
	p.pending = p.pending[1:]
	return r, true
}

// waitIdle returns when no check is pending or running (or ctx ends).
func (p *paramChecker) waitIdle(ctx context.Context) {
	for {
		p.mu.Lock()
		idle := len(p.queued) == 0
		p.mu.Unlock()
		if idle {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollIdle):
		}
	}
}

// want queues refs that were never checked or whose check is stale.
func (p *paramChecker) want(refs []paramRef) {
	p.mu.Lock()
	p.last = refs
	added := false
	for _, r := range refs {
		if r.selector || r.name == "" || p.queued[r.key()] {
			continue
		}
		if st, ok := p.results[r.key()]; ok && time.Since(st.checked) < recheckAfter {
			continue
		}
		p.pending = append(p.pending, r)
		p.queued[r.key()] = true
		added = true
	}
	p.mu.Unlock()
	if added {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *paramChecker) check(ctx context.Context, r paramRef) paramStatus {
	st := paramStatus{checked: time.Now()}
	rs, err := p.resources(r.apiVersion)
	if err != nil {
		st.err = err
		return st
	}
	_, v := enginekit.ParseAPIVersion(r.apiVersion)
	for _, res := range rs {
		if res.Kind != r.kind || (v != "" && res.GVR.Version != v) {
			continue
		}
		if res.Namespaced && r.namespace == "" {
			st.perRequest = true // read from each request's namespace
			return st
		}
		ctx, cancel := context.WithTimeout(ctx, enginekit.ProbeTimeout)
		ri := p.cluster.Dynamic().Resource(res.GVR)
		if res.Namespaced {
			_, err = ri.Namespace(r.namespace).Get(ctx, r.name, metav1.GetOptions{})
		} else {
			_, err = ri.Get(ctx, r.name, metav1.GetOptions{})
		}
		cancel()
		switch {
		case err == nil:
			st.found = true
		case apierrors.IsNotFound(err):
		default:
			st.err = err
		}
		return st
	}
	st.err = fmt.Errorf("param kind %s %s is not served", r.apiVersion, r.kind)
	return st
}

// resources lists what apiVersion serves: the core group ("v1") answers
// only to its group-version; named groups go through GroupResources.
func (p *paramChecker) resources(apiVersion string) (map[string]enginekit.Resource, error) {
	g, v := enginekit.ParseAPIVersion(apiVersion)
	if g != "" {
		return enginekit.GroupResources(p.cluster.Discovery(), g)
	}
	l, err := p.cluster.Discovery().ServerResourcesForGroupVersion(v)
	if err != nil {
		return nil, err
	}
	out := map[string]enginekit.Resource{}
	for _, r := range l.APIResources {
		if !strings.Contains(r.Name, "/") {
			out[r.Name] = enginekit.Resource{GVR: schema.GroupVersionResource{Version: v, Resource: r.Name}, Kind: r.Kind, Namespaced: r.Namespaced}
		}
	}
	return out, nil
}

// issues reports missing params: with parameterNotFoundAction Deny every
// matching request is rejected; with Allow the binding does nothing.
func (p *paramChecker) issues(refs []paramRef) []model.HealthIssue {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []model.HealthIssue
	for _, r := range refs {
		st, ok := p.results[r.key()]
		if !ok || r.selector || r.name == "" || st.perRequest {
			continue
		}
		obj := r.kind + " " + nsName(r.namespace, r.name)
		switch {
		case st.err != nil:
			out = append(out, model.HealthIssue{Engine: Name, Severity: model.SeverityInfo, Category: "params", Object: r.binding,
				Message: fmt.Sprintf("could not check param %s: %v", obj, st.err)})
		case !st.found && r.notFound == "Allow":
			out = append(out, model.HealthIssue{Engine: Name, Severity: model.SeverityWarning, Category: "params", Object: r.binding,
				Message: fmt.Sprintf("param %s does not exist and parameterNotFoundAction is Allow: the binding checks nothing", obj)})
		case !st.found:
			out = append(out, model.HealthIssue{Engine: Name, Severity: model.SeverityError, Category: "params", Object: r.binding,
				Message: fmt.Sprintf("param %s does not exist and parameterNotFoundAction is Deny: every matching request is rejected", obj)})
		}
	}
	return out
}

func nsName(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}
