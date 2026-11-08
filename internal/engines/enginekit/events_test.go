package enginekit_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engines/enginekit"
)

func TestEventTimeAndCount(t *testing.T) {
	ev := func(fields map[string]any) *unstructured.Unstructured {
		o := map[string]any{"metadata": map[string]any{"creationTimestamp": "2026-11-08T09:00:00Z"}}
		for k, v := range fields {
			o[k] = v
		}
		return &unstructured.Unstructured{Object: o}
	}
	cases := []struct {
		name  string
		ev    *unstructured.Unstructured
		at    string
		count int
	}{
		{"core", ev(map[string]any{"lastTimestamp": "2026-11-08T11:00:00Z", "eventTime": "2026-11-08T10:00:00Z", "count": int64(4)}), "11:00", 4},
		{"series latest over first", ev(map[string]any{"eventTime": "2026-11-08T10:00:00.5Z",
			"series": map[string]any{"lastObservedTime": "2026-11-08T10:30:00Z", "count": int64(7)}}), "10:30", 7},
		{"zero eventTime falls back", ev(map[string]any{"eventTime": "0001-01-01T00:00:00Z"}), "09:00", 1},
		{"nothing", ev(nil), "09:00", 1},
	}
	for _, c := range cases {
		if got := enginekit.EventTime(c.ev).UTC().Format("15:04"); got != c.at {
			t.Errorf("%s: EventTime = %s, want %s", c.name, got, c.at)
		}
		if got := enginekit.EventCount(c.ev); got != c.count {
			t.Errorf("%s: EventCount = %d, want %d", c.name, got, c.count)
		}
	}
}
