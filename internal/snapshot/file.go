// Package snapshot records what p9s read from a cluster and replays it.
//
// "p9s snapshot" runs a normal sync through recording clients: every list
// and get answer (and every RBAC denial) the engines, owner resolver and
// health and events watchers received is kept, with the discovery answers
// they used. "--snapshot" replays the file through fake clients, so the
// real engines rebuild exactly the views, details and drill-downs they
// showed, offline. Secret data is never recorded.
package snapshot

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/model"
)

// SchemaVersion identifies the file format.
const SchemaVersion = "p9s/snapshot/v1"

// File is a recorded cluster.
type File struct {
	SchemaVersion string            `json:"schemaVersion"`
	TakenAt       time.Time         `json:"takenAt"`
	P9sVersion    string            `json:"p9sVersion"`
	Context       model.KubeContext `json:"context"`
	// Groups and Resources are the discovery answers engines asked for.
	Groups    *metav1.APIGroupList      `json:"groups,omitempty"`
	Resources []*metav1.APIResourceList `json:"resources"`
	// Preferred is the cluster's preferred-version discovery (all kinds),
	// for reports on what no policy covers.
	Preferred []*metav1.APIResourceList `json:"preferred,omitempty"`
	// PreferredError is set when that discovery was partial or failed.
	PreferredError string            `json:"preferredError,omitempty"`
	Objects        []Objects         `json:"objects"`
	Metadata       []MetadataObjects `json:"metadata,omitempty"`
	Denied         []Denial          `json:"denied,omitempty"`
}

// Objects are the recorded items of one resource.
type Objects struct {
	Group    string                       `json:"group"`
	Version  string                       `json:"version"`
	Resource string                       `json:"resource"`
	Items    []*unstructured.Unstructured `json:"items"`
}

// MetadataObjects are metadata-only items (owner resolution).
type MetadataObjects struct {
	Group    string                         `json:"group"`
	Version  string                         `json:"version"`
	Resource string                         `json:"resource"`
	Items    []metav1.PartialObjectMetadata `json:"items"`
}

// Denial is a list the API server refused, cluster-wide (Namespace "")
// or in one namespace.
type Denial struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Metadata  bool   `json:"metadata,omitempty"` // denied to the metadata client
	Message   string `json:"message"`
}

// Write encodes f.
func (f *File) Write(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(f)
}

// WriteFile writes f to path atomically (temp file, fsync, rename), mode
// 0600 even when replacing a looser file: snapshots describe a cluster.
func (f *File) WriteFile(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".p9s-snapshot-*")
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := f.Write(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("snapshot: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	return nil
}

// Read loads and checks a snapshot.
func Read(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", path, err)
	}
	if f.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("snapshot %s: schema %q, want %q", path, f.SchemaVersion, SchemaVersion)
	}
	return &f, nil
}
