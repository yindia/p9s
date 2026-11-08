// Package fixtures provides ready-made fake engines with deterministic data.
package fixtures

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/model"
)

// Now is the fixed clock the fixtures are relative to.
var Now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// AllCaps enables every read capability.
var AllCaps = engine.Capabilities{Policies: true, Violations: true, Mutations: true, Health: true}

// Alpha is a fully capable fake engine with policies in every mode.
func Alpha() *fake.Engine {
	pol := func(name string, mode model.EnforcementMode, raw string, n int, audit time.Duration) model.Policy {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "alpha.example/v1", "kind": "AlphaRule",
			"metadata": map[string]any{"name": name},
			"spec":     map[string]any{"mode": raw},
		}}
		var last time.Time
		if audit > 0 {
			last = Now.Add(-audit)
		}
		return model.Policy{
			Engine: "alpha", Kind: "AlphaRule", Name: name,
			Enforcement: model.Enforcement{Mode: mode, Raw: raw}, ViolationCount: n, LastAudit: last,
			ViolationsKnown: audit > 0, // never audited: no count to show
			Sources:         []model.Source{{Language: model.LangYAML, Code: "rule: " + name, Origin: "AlphaRule " + name}},
			Raw:             obj,
		}
	}
	key := model.PolicyKey{Engine: "alpha", Kind: "AlphaRule", Name: "require-owner"}
	return fake.New(fake.Options{
		Name: "alpha", Prefix: "al", Installed: true, Version: "v1.2.0", Caps: AllCaps,
		Data: fake.Data{
			Policies: []model.Policy{
				withGuidance(pol("require-owner", model.ModeEnforce, "block", 2, 30*time.Second), model.GuidanceItem{
					From: "annotation alpha.example/description", Title: "Require an owner",
					Text: "Add an owner label naming the team that runs it, e.g. owner=payments.",
				}),
				pol("prefer-limits", model.ModeWarn, "warn", 0, 90*time.Second),
				pol("observe-images", model.ModeAudit, "observe", 0, 0),
			},
			Violations: []model.Violation{
				{Engine: "alpha", Policy: key, Rule: "owner-label", Enforcement: model.Enforcement{Mode: model.ModeEnforce, Raw: "block"},
					Target: model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "payments"}, Message: "label owner is required", Timestamp: Now.Add(-30 * time.Second)},
				{Engine: "alpha", Policy: key, Rule: "owner-label", Enforcement: model.Enforcement{Mode: model.ModeEnforce, Raw: "block"},
					Target: model.ObjectRef{Version: "v1", Kind: "Namespace", Name: "search"}, Message: "label owner is required", Timestamp: Now.Add(-30 * time.Second)},
			},
			Mutations: []model.Mutation{{Engine: "alpha", Kind: "AlphaPatch", Name: "default-pull-policy", Location: "spec.containers[*].imagePullPolicy", Targets: "Pod"}},
			Health:    []model.HealthIssue{{Engine: "alpha", Severity: model.SeverityWarning, Category: "audit", Object: model.ObjectRef{Kind: "AlphaRule", Name: "observe-images"}, Message: "never audited"}},
		},
	})
}

// Beta is a second engine with only policies and violations, to prove the
// UI aggregates across engines and hides what an engine cannot do.
func Beta() *fake.Engine {
	key := model.PolicyKey{Engine: "beta", Kind: "BetaPolicy", Namespace: "payments", Name: "no-latest"}
	return fake.New(fake.Options{
		Name: "beta", Prefix: "bt", Installed: true, Version: "0.9.1",
		Caps: engine.Capabilities{Policies: true, Violations: true},
		Data: fake.Data{
			Policies: []model.Policy{{
				Engine: "beta", Kind: "BetaPolicy", Namespace: "payments", Name: "no-latest",
				Enforcement: model.Enforcement{Mode: model.ModeAudit, Raw: "Audit"}, ViolationCount: 1, LastAudit: Now.Add(-10 * time.Second),
				ViolationsKnown: true,
			}},
			Violations: []model.Violation{{
				Engine: "beta", Policy: key, Rule: "tag", Enforcement: model.Enforcement{Mode: model.ModeAudit, Raw: "Audit"},
				Target: model.ObjectRef{Version: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"}, Message: "image tag latest is not allowed", Timestamp: Now.Add(-10 * time.Second),
			}},
			Access: []model.AccessIssue{{Engine: "beta", Resource: "betareports.beta.example", Verb: "list", Message: "forbidden"}},
		},
	})
}

func withGuidance(p model.Policy, g ...model.GuidanceItem) model.Policy {
	p.Guidance = g
	return p
}
