package vap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/yindia/p9s/internal/engines/vap/vaptest"
)

func TestWantNeverDropsRefs(t *testing.T) {
	p := newParamChecker(vaptest.NewCluster(t, vaptest.Options{}))
	var refs []paramRef
	for i := range 1000 {
		refs = append(refs, paramRef{apiVersion: "v1", kind: "ConfigMap", namespace: "default", name: fmt.Sprint("p", i)})
	}
	p.want(refs)
	if len(p.pending) != 1000 || len(p.queued) != 1000 {
		t.Fatalf("pending %d queued %d: every ref is kept until checked", len(p.pending), len(p.queued))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.start(ctx, func() {})
	done := make(chan struct{})
	go func() { p.waitIdle(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("never idle")
	}
	if len(p.results) != 1000 {
		t.Errorf("results = %d", len(p.results))
	}
}

func TestCancelledCheckIsNotAResult(t *testing.T) {
	p := newParamChecker(vaptest.NewCluster(t, vaptest.Options{}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.want([]paramRef{{apiVersion: "v1", kind: "ConfigMap", namespace: "default", name: "x"}})
	p.run(ctx) // returns at once on a cancelled context
	if len(p.results) != 0 {
		t.Errorf("results = %+v: a cancelled lookup must not be cached", p.results)
	}
}
