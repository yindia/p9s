package views_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yindia/p9s/internal/engines/fake/fixtures"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/views"
)

func fixtureReport(t *testing.T) views.Report {
	t.Helper()
	kinds := func() ([]model.APIResource, error) {
		return []model.APIResource{{Kind: "Namespace", Resource: "namespaces"}, {Kind: "ConfigMap", Resource: "configmaps", Namespaced: true}}, nil
	}
	return views.ReportOf(env(t, ""), views.ReportMeta{Cluster: "kind", P9sVersion: "test", Generated: fixtures.Now}, kinds)
}

func TestReportOfFixtures(t *testing.T) {
	r := fixtureReport(t)
	if r.Header.Context != "kind" || r.Header.Cluster != "kind" || len(r.Header.Engines) != 1 {
		t.Errorf("header = %+v", r.Header)
	}
	if r.Summary.Violations != 3 || r.Summary.UnknownPolicies != 1 || r.Summary.PoliciesByMode["enforce"] != 1 {
		t.Errorf("summary = %+v", r.Summary)
	}
	if len(r.PSS.Controls) == 0 || len(r.PSS.Uncategorised) != 4 {
		t.Errorf("pss: %d controls, uncategorised %v", len(r.PSS.Controls), r.PSS.Uncategorised)
	}
	if len(r.Violations.ByNamespace) == 0 || r.Violations.BySeverity[0].Name != "unspecified" {
		t.Errorf("violations = %+v", r.Violations)
	}
	if len(r.Items) != 3 || len(r.Policies) != 4 || len(r.Omitted) == 0 {
		t.Errorf("items %d policies %d omitted %v", len(r.Items), len(r.Policies), r.Omitted)
	}
}

func TestSealIsReproducible(t *testing.T) {
	r := fixtureReport(t)
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	sum := r.Header.BodySHA256
	r.Header.BodySHA256 = ""
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != sum || len(sum) != 64 {
		t.Errorf("hash %s does not verify by the documented recipe", sum)
	}
	r2 := fixtureReport(t)
	_ = r2.Seal()
	if r2.Header.BodySHA256 != sum {
		t.Error("same inputs, different hash")
	}
}

func TestDiff(t *testing.T) {
	k1 := model.PolicyKey{Engine: "a", Kind: "R", Name: "p1"}
	prev := views.Report{
		Policies: []views.ReportPolicy{{Key: k1, Mode: "audit"}},
		Items:    []views.ReportViolation{{ID: "old"}, {ID: "kept"}},
	}
	cur := views.Report{
		Policies: []views.ReportPolicy{{Key: k1, Mode: "enforce"}},
		Items:    []views.ReportViolation{{ID: "kept"}, {ID: "new"}},
	}
	cur.Violations.Truncated = true
	d := views.Diff(prev, cur)
	if len(d.NewViolations) != 1 || d.NewViolations[0].ID != "new" || len(d.ResolvedViolations) != 1 || d.ResolvedViolations[0].ID != "old" {
		t.Errorf("diff = %+v", d)
	}
	if len(d.EnforcementChanged) != 1 || d.EnforcementChanged[0].From != "audit" || d.EnforcementChanged[0].To != "enforce" {
		t.Errorf("mode changes = %+v", d.EnforcementChanged)
	}
	if d.Comparable {
		t.Error("a truncated list must make the diff not comparable")
	}
}

func TestDiffKeepsAViolationWhoseMessageChanged(t *testing.T) {
	k := model.PolicyKey{Engine: "a", Kind: "R", Name: "p"}
	prev := views.Report{Policies: []views.ReportPolicy{{Key: k}, {Key: model.PolicyKey{Engine: "a", Kind: "R", Name: "gone"}}},
		Items: []views.ReportViolation{{ID: "a:R/p||Pod/x/y", Message: "3 replicas"}}}
	cur := views.Report{Policies: []views.ReportPolicy{{Key: k}, {Key: model.PolicyKey{Engine: "a", Kind: "R", Name: "added"}}},
		Items: []views.ReportViolation{{ID: "a:R/p||Pod/x/y", Message: "4 replicas"}}}
	d := views.Diff(prev, cur)
	if len(d.NewViolations) != 0 || len(d.ResolvedViolations) != 0 || len(d.ChangedMessages) != 1 {
		t.Errorf("diff = %+v: a persisting violation with a new message is neither new nor resolved", d)
	}
	if len(d.AddedPolicies) != 1 || d.AddedPolicies[0].Name != "added" || len(d.RemovedPolicies) != 1 || d.RemovedPolicies[0].Name != "gone" {
		t.Errorf("policies added %v removed %v", d.AddedPolicies, d.RemovedPolicies)
	}
}

func TestReportViolationIDIgnoresMessage(t *testing.T) {
	r := fixtureReport(t)
	for _, it := range r.Items {
		if strings.Contains(it.ID, it.Message) && it.Message != "" {
			t.Errorf("ID %q embeds the message", it.ID)
		}
	}
}
