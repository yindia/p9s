package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultSyncTimeout bounds how long non-interactive commands wait.
const DefaultSyncTimeout = 60 * time.Second

// syncPollInterval is how often WaitForSync re-reads in-memory sync state.
// This never touches the API server; informers do the watching.
const syncPollInterval = 50 * time.Millisecond

// Synced reports whether every active engine finished its initial lists.
func (s *Session) Synced() bool { return len(s.Pending()) == 0 }

// Pending maps active engines that are not synced to their loading resources.
func (s *Session) Pending() map[string][]string {
	out := map[string][]string{}
	for _, e := range s.Engines() {
		if st := e.SyncStatus(); !st.Synced {
			out[e.Name()] = st.Pending
		}
	}
	if o := s.Owners(); o != nil {
		if st := o.SyncStatus(); !st.Synced {
			out["owners"] = st.Pending
		}
	}
	if e := s.Events(); e != nil {
		if st := e.SyncStatus(); !st.Synced {
			out["events"] = st.Pending
		}
	}
	if p := s.Platform(); p != nil {
		if st := p.SyncStatus(); !st.Synced {
			out["platform"] = st.Pending
		}
	}
	return out
}

// SyncTimeoutError names what did not sync in time.
type SyncTimeoutError struct {
	Timeout time.Duration
	Pending map[string][]string
}

func (e *SyncTimeoutError) Error() string {
	engines := make([]string, 0, len(e.Pending))
	for name := range e.Pending {
		engines = append(engines, name)
	}
	sort.Strings(engines)
	parts := make([]string, 0, len(engines))
	for _, name := range engines {
		parts = append(parts, fmt.Sprintf("%s (%s)", name, strings.Join(e.Pending[name], ", ")))
	}
	return fmt.Sprintf("caches did not sync within %s: %s", e.Timeout, strings.Join(parts, "; "))
}

// WaitForSync blocks until every active engine is synced, ctx ends, or
// timeout passes; on timeout it returns a *SyncTimeoutError.
func (s *Session) WaitForSync(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(syncPollInterval)
	defer tick.Stop()
	for {
		pending := s.Pending()
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return &SyncTimeoutError{Timeout: timeout, Pending: pending}
			}
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// RunOnce is the one-shot mode shared by non-interactive commands: connect,
// start informers, wait for sync, and hand back a session ready to read. The
// caller must Close it. On any error the session is already closed.
func RunOnce(ctx context.Context, opts Options, syncTimeout time.Duration) (*Session, error) {
	if syncTimeout <= 0 {
		syncTimeout = DefaultSyncTimeout
	}
	s, err := New(opts)
	if err != nil {
		return nil, err
	}
	if err := s.Connect(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.WaitForSync(ctx, syncTimeout); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
