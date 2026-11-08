package views

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/model"
	"github.com/yindia/p9s/internal/query"
	"github.com/yindia/p9s/internal/table"
)

// CmdImpact is the enforcement impact of one policy (i on a policy row,
// or ":impact <name>").
const CmdImpact = "impact"

// impactNamespaces bounds the "by namespace" headline.
const impactNamespaces = 5

// ImpactSpec builds the impact view of one policy. It is cluster-wide:
// the :ns scope does not apply, so the counts match the engine's total.
func ImpactSpec(env Env, key model.PolicyKey) Spec {
	impact := func() (query.Impact, error) { return query.ImpactOf(env.Engines(), key, env.lookup()) }
	return Spec{Source: table.Source{
		View: impactView(key, impact),
		Summary: func(engine.Filter) []string {
			im, err := impact()
			if err != nil && errors.Is(err, query.ErrPolicyNotFound) {
				return nil
			}
			return impactSummary(im)
		},
		Notices: func(engine.Filter) []string {
			// RBAC gaps in the engine's audit data make the impact partial.
			n := query.AccessNotices(query.Capable(env.Engines(), engine.Filter{Engine: key.Engine}, query.HasViolations))
			if env.Scope() != "" {
				n = append(n, "impact is cluster-wide: the :ns scope does not apply")
			}
			if o := env.owners(); o != nil {
				if msg := ownerNotice(o.AccessIssues()); msg != "" {
					n = append(n, msg)
				}
			}
			return n
		},
		Empty: func(engine.Filter) string {
			im, err := impact()
			switch {
			case errors.Is(err, query.ErrPolicyNotFound):
				return fmt.Sprintf("Policy %s is not loaded by any active engine.", key)
			case !im.Known:
				return "No audit data for this policy (never audited, background scans off, or it does not validate): its impact is unknown."
			}
			if im.EvalErrors > 0 {
				return "No violations: only results that failed to evaluate (see :health)."
			}
			return "Nothing that exists today violates it: enforcing would block no current object."
		},
		Synced: func(engine.Filter) bool { return query.Synced(env.Engines()) && env.ownersSynced() },
	}}
}

// IsImpact reports whether a spec is an impact view.
func IsImpact(s Spec) bool { return s.Command() == CmdImpact }

func impactView(key model.PolicyKey, impact func() (query.Impact, error)) engine.View {
	return engine.View{
		Command: CmdImpact, Title: "Impact: " + key.Name,
		Columns: []engine.Column{
			{Title: "NAMESPACE", Role: engine.RoleNamespace}, {Title: "OWNER", Role: engine.RoleTarget},
			{Title: "MANAGED"}, {Title: "OBJECTS", Role: engine.RoleCount},
			{Title: "MESSAGE", Role: engine.RoleMessage, Flex: true},
		},
		Rows: func(engine.Filter) ([]engine.Row, error) {
			im, err := impact()
			if errors.Is(err, query.ErrPolicyNotFound) {
				return nil, nil // the empty state says so
			}
			rows := make([]engine.Row, 0, len(im.Owners))
			for _, o := range im.Owners {
				rows = append(rows, impactRow(key, o))
			}
			return rows, err
		},
	}
}

func impactRow(key model.PolicyKey, o query.ImpactOwner) engine.Row {
	owner := o.Owner
	return engine.Row{
		ID: owner.String(),
		Cells: []engine.Cell{
			engine.TextCell(namespaceLabel(o.Namespace)), engine.TextCell(owner.String()),
			engine.TextCell(managedLabel(o.Managed)), engine.CountCell(o.Objects),
			engine.TextCell(table.OneLine(o.FirstMessage())),
		},
		Ref: &owner,
		Drill: &engine.Target{Command: CmdViolationsFlat, Filter: engine.Filter{
			Engine: key.Engine, PolicyKind: key.Kind, PolicyName: key.Name, PolicyNamespace: key.Namespace,
			Owner: owner.String(),
		}},
	}
}

func namespaceLabel(ns string) string {
	if ns == "" {
		return "(cluster)"
	}
	return ns
}

func managedLabel(m query.Managed) string {
	switch m {
	case query.ManagedController:
		return "controller"
	case query.ManagedBare:
		return "bare"
	}
	return "-"
}

// impactSummary is the headline: what enforcing would block, how complete
// the numbers are, and how the owners split.
func impactSummary(im query.Impact) []string {
	p := im.Policy
	name := p.Kind + "/" + p.Name
	if p.Namespace != "" {
		name = p.Kind + "/" + p.Namespace + "/" + p.Name
	}
	lines := []string{fmt.Sprintf("%s %s · %s", p.Engine, name, p.Enforcement.String())}
	switch {
	case !im.Known && im.Listed == 0:
		return append(lines, "Impact unknown: the engine has no audit data for this policy.")
	case !im.Known:
		lines = append(lines, fmt.Sprintf("The engine gives no count for this policy: below are the %s it lists, which may not be all.",
			plural(im.Listed, "violation")))
	case im.Total == 0:
		lines = append(lines, "If enforced: nothing that exists today violates it.")
	default:
		lines = append(lines, countLines(im)...)
	}
	if im.EvalErrors > 0 {
		lines = append(lines, fmt.Sprintf("%s failed to evaluate (not violations; see :health).", plural(im.EvalErrors, "result")))
	}
	if len(im.Owners) > 0 {
		lines = append(lines, managedLine(im), namespacesLine(im))
	}
	return lines
}

// countLines states what enforcing blocks and how complete the list is.
func countLines(im query.Impact) []string {
	what := plural(im.Total, "violation")
	if im.Complete {
		what += " on " + plural(im.Objects, "object")
	}
	var lines []string
	if im.Enforcing() {
		lines = append(lines, "Already enforcing: "+what+" exist today. Matching creates and updates of them are rejected.")
	} else {
		lines = append(lines, "If enforced: "+what+". Matching creates and updates would be rejected; running objects keep running.")
	}
	if !im.Complete {
		lines = append(lines, fmt.Sprintf("Showing %d of %d (%s): the groups below cover the listed %d only.",
			im.Listed, im.Total, truncationReason(im.Policy.Engine), im.Listed))
	}
	return lines
}

func truncationReason(engineName string) string {
	if engineName == "gatekeeper" {
		return "Gatekeeper lists at most --constraint-violations-limit"
	}
	return "the engine lists fewer than it counts"
}

func managedLine(im query.Impact) string {
	parts := []string{fmt.Sprintf("%d controller-managed (new pods, rollouts)", im.ByManaged[query.ManagedController]),
		fmt.Sprintf("%d bare (re-create, update)", im.ByManaged[query.ManagedBare])}
	if n := im.ByManaged[query.ManagedUnknown]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown (ownership not cached)", n))
	}
	return "Owners whose requests may be rejected: " + strings.Join(parts, " · ")
}

func namespacesLine(im query.Impact) string {
	var parts []string
	for i, n := range im.Namespaces {
		if i == impactNamespaces {
			parts = append(parts, fmt.Sprintf("+%d more", len(im.Namespaces)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", namespaceLabel(n.Name), n.Objects))
	}
	return "Objects by namespace: " + strings.Join(parts, " · ")
}

// ResolveImpact resolves ":impact <name>" to one policy.
func ResolveImpact(env Env, name string) (Spec, engine.Filter, error) {
	if name == "" {
		return Spec{}, engine.Filter{}, errors.New(":impact needs a policy: name, kind/name or engine:kind/name (or press i on a policy row)")
	}
	keys, err := query.FindPolicies(env.Engines(), name)
	switch {
	case len(keys) == 1:
		return ImpactSpec(env, keys[0]), engine.Filter{}, nil
	case len(keys) > 1:
		names := make([]string, 0, len(keys))
		for _, k := range keys {
			names = append(names, k.String())
		}
		return Spec{}, engine.Filter{}, fmt.Errorf("%q is ambiguous; use one of: %s", name, strings.Join(names, ", "))
	case err != nil:
		return Spec{}, engine.Filter{}, err
	}
	return Spec{}, engine.Filter{}, fmt.Errorf("no policy %q in the active engines", name)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
