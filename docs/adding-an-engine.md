# Adding a policy engine

p9s is engine-agnostic. An engine is one package under
`internal/engines/<name>/` that implements `engine.Engine`. The shared views
(`:pol`, `:v`, `:mut`, `:health`, the overview), `p9s get`, `snapshot`,
`report` and the TUI pick it up without changes: adding an engine never
touches `internal/ui`, and `internal/boundary_test.go` fails the build if the
UI starts importing engine code.

Three engines show the shapes an engine can take:

| Engine | Shape | Read it for |
|---|---|---|
| `vap` | two shared informers, one optional, background lookups | **the smallest complete example**: start here |
| `kyverno` | a fixed set of CRDs across two API families; violations in separate PolicyReports joined back to policies | resolving results to policies, admission Events, metadata guidance |
| `gatekeeper` | policy kinds are CRDs discovered at runtime, with their own per-kind informers; violations in each policy's status | dynamic informers, capped violation lists, reading controller flags |

## Contents

1. [The pieces](#the-pieces)
2. [Lifecycle](#lifecycle)
3. [The interface](#the-interface)
4. [Normalising data](#normalising-data)
5. [Informers, sync and RBAC](#informers-sync-and-rbac)
6. [Views](#views)
7. [Optional interfaces](#optional-interfaces)
8. [Registering](#registering)
9. [Testing](#testing)
10. [Checklist](#checklist)

## The pieces

| Package | Role |
|---|---|
| `internal/model` | engine-neutral types: `Policy`, `Violation`, `Mutation`, `HealthIssue`, `AccessIssue`, `AdmissionEvent`, `ObjectRef`, `Enforcement` |
| `internal/engine` | the `Engine` interface, optional interfaces, `Capabilities`, `View`/`Row`/`Cell`/`Detail`, `Filter`, the registry |
| `internal/engine/enginetest` | the contract suite every engine runs against itself |
| `internal/engines/enginekit` | shared plumbing: `Access` (RBAC probes, informers, watch errors, namespace fallback), `Sync` (sync tracking), the cache `Transform`, discovery helpers (`PreferredVersions`, `GroupResources`), `ResourceRules`, `Objects`, YAML/KV helpers |
| `internal/engines/enginekit/fakecluster` | a fake `engine.Cluster` for tests: thread-safe discovery, seeding under real plurals, `Forbid`, `ForbidClusterWide`, owner chains |
| `internal/engines/<name>` | your engine, plus a `<name>test` fixture package |

## Lifecycle

```
New(cluster) ─▶ Detect(ctx) ─▶ Start(ctx, factory, notify) ─▶ reads… ─▶ Stop()
```

- **New** (the factory) runs at startup and again on every context switch,
  with clients for that context. Keep no state across factories.
- **Detect** answers "is this engine installed here?" from discovery and
  returns a version (from the controller image, `"unknown"` when RBAC hides
  it; `vap` returns `"builtin"`). Return an error only when detection
  itself failed (the API is unreachable), not when the engine is absent.
  Detect may also read controller flags (Gatekeeper's `--audit-interval`,
  Kyverno's `--omitEvents`); store them for reads.
- **Start** registers informers on the shared factory (the session starts it
  after every engine registered) and runs any informers of its own. Call
  `notify()` from event handlers; the session coalesces bursts. **Never
  poll.**
- **Reads** (`Policies`, `Violations`, `Health`, view rows, …) are called on
  every update from the UI goroutine. They must be cheap, safe for
  concurrent use, and **never call the API server**. Build a snapshot from
  the informer caches and cache it until the next change (`vap` keys its
  snapshot on a generation counter bumped by `notify`).
- **Stop** stops everything Start began, resets sync, and must be
  idempotent.

## The interface

```go
type Engine interface {
    Name() string                 // "vap": lowercase, stable
    Prefix() string               // "vap": namespaces ":vap/..." commands
    Capabilities() Capabilities

    Detect(ctx) (installed bool, version string, err error)
    Start(ctx, factory, notify) error
    Stop()
    SyncStatus() SyncStatus       // Synced once every initial list finished; Pending names the rest

    Policies() ([]model.Policy, error)
    Violations() ([]model.Violation, error)
    Mutations() ([]model.Mutation, error)    // ErrNotSupported unless Capabilities().Mutations
    Health() ([]model.HealthIssue, error)
    AccessIssues() []model.AccessIssue

    Views() []View                           // engine-specific ":prefix/..." tables
    Evaluator() (Evaluator, error)           // ErrNotSupported unless Capabilities().Evaluator
    Actions() (Actions, error)               // ErrNotSupported unless Capabilities().Actions
}
```

Set a capability only when the matching method returns data. Views and
actions are hidden for capabilities an engine lacks, and the contract checks
that unset ones return `engine.ErrNotSupported`.

## Normalising data

Shared views, the report and `get` show every engine the same way, so map
your engine's terms onto the model and keep its own in the detail.

**Policies**

| Field | Rule |
|---|---|
| `Enforcement` | `Mode` is one of `enforce`, `warn`, `audit`, `mutate`, `unknown`; `Raw` keeps your term (`deny`, `Enforce`, `Deny,Warn`). With several actions the strictest wins. `Enforcement.String()` renders `enforce (engine: deny)` |
| `ViolationCount`, `ViolationsKnown` | set `ViolationsKnown` only when the engine really has a count (it was audited, it validates). Otherwise every view shows `-`, never 0 |
| `LastAudit` | zero when never audited |
| `Sources` | policy code with a `Language` (`rego`, `cel`, `yaml`) for highlighting |
| `Guidance` | how-to-fix text from the policy's own metadata, each item with where it came from. Never invent it |
| `Severity` | from policy metadata, `""` when the policy declares none |
| `Matches`, `MatchesAll`, `MatchKnown` | what kinds or resources the policy selects (for the report's unprotected kinds). `enginekit.ResourceRules` parses admission-style `resourceRules`. Leave `MatchKnown` false when you cannot tell |
| `Raw` | the object, for `y` and `e` |

**Violations**

- `Target` carries group, version, kind, namespace and name: `g` resolves
  it through discovery, and owners are resolved from it.
- Set `Truncated` when the engine listed fewer violations than it counted
  (Gatekeeper's `--constraint-violations-limit`); views then say
  "showing N of M" and never present the list as complete.
- Set `EvalError` for results where the policy failed to evaluate (Kyverno
  `error` results): they are not violations of the object.
- Set `PolicyUnreadable` when RBAC hides the policy and its key was
  inferred (see [RBAC](#rbac)).

**Health**

`HealthIssue{Severity, Category, Object, Message}`. The category decides
where it appears in `:health`: `compile`, `template`, `policy`, `discovery`,
`constraint`, `bindings`, `params` and `type checking` go into the Policies
row's detail, everything else into the Audit row's. `bindings` and `params`
errors make the Policies row critical. Engine-wide issues have no `Object`.

## Informers, sync and RBAC

### Informers

Use `enginekit.Access.SharedInformer` for every resource you read:

```go
e.access = enginekit.NewAccess(Name)
e.access.SetHomeNamespace(engine.HomeNamespace(cluster))

inf := e.access.SharedInformer(ctx, dyn, factory, gvr, notify, notify)
if inf != nil {             // nil: RBAC denied it, already recorded
    e.sync.Track(gvr, inf)
}
```

It probes RBAC with `List(limit=1)`, installs the cache `Transform` (drops
`managedFields` and kubectl's last-applied copy), sets a watch error handler
that records denials, and falls back to the home namespace for a tenant (see
below). Informers you build yourself (Gatekeeper's per-kind ones, an Event
watch with a field selector) must call `enginekit.SetTransform`, a
`WatchErrorHandler`, and `Track`/`TrackAs` themselves. The shared factory
dedupes informers, so two engines reading PolicyReports share one.

### Sync

`enginekit.Sync` tells the UI and `p9s get`/`report` when data is final:

```go
registered := e.sync.Start()       // pending until every informer is tracked
e.sync.SkipWhen(e.access.Denied)   // a denied resource never blocks sync
// … Track / TrackAs each informer …
done := e.sync.Gate("param checks") // your own conditions; call done() when met
registered()
e.sync.Watch(ctx, notify)          // notifies once synced, even if every list was empty
```

Until synced, shared views draw counts as provisional (`~12`) and the
header reads `syncing…`; non-interactive commands wait, with a timeout that
names your pending resources. `Stop` calls `e.sync.Reset()`.

Background work belongs behind a `Gate`: `vap` looks up param objects in a
worker (never on a read) and opens its gate after the first round, so a
`report` never finishes before the checks do.

### RBAC

Never fail because something is forbidden; report it through
`AccessIssues()` and keep going.

- Denials become notices above the affected tables, and in `get` and
  `report` output.
- **Namespace-only access**: when a namespaced resource cannot be listed
  cluster-wide but can be in the kubeconfig context's namespace,
  `SharedInformer` watches that namespace instead and records an
  `AccessIssue` with `OnlyNamespace` set ("showing namespace X only").
  `Access.Scoped` and `Access.Denied` tell the two cases apart.
- If your violations can outlive access to their policies (Kyverno reports
  are namespaced, policies are cluster-wide), keep them with an inferred
  key and `PolicyUnreadable: true` rather than dropping them.

## Views

`Views()` returns tables the renderers draw generically.

```go
engine.View{
    Command: Prefix + "/pol", Aliases: []string{Prefix + "/policies"},
    Title: "ValidatingAdmissionPolicies", Description: "…",
    Columns: []engine.Column{
        {Title: "NAME", Role: engine.RoleName}, {Title: "BINDINGS", Role: engine.RoleCount},
        {Title: "ENFORCEMENT", Role: engine.RoleMode}, {Title: "GENERATED BY", Flex: true},
    },
    DefaultSort: engine.Sort{Role: engine.RoleName},
    Rows: func(f engine.Filter) ([]engine.Row, error) { … },
    Keys: []engine.ViewKey{{Key: "G", Help: "generated", Do: toggle}},
}
```

- `Command` and aliases start with `Prefix()+"/"`.
- **Roles** (`RoleName`, `RoleKind`, `RoleEngine`, `RoleNamespace`,
  `RoleMode`, `RoleCount`, `RoleAge`, `RoleSeverity`, `RoleMessage`,
  `RoleTarget`) drive the sort keys, `/` filter terms and the message line
  under the table. Each appears at most once per view; `DefaultSort` must
  name one the view has. Put the name column before the kind.
- **Rows** apply the policy fields of `engine.Filter` and its `Namespace`
  scope; the renderer applies `Filter.Text`. Return nothing when
  `f.Engine` names another engine.
- **Cells** are typed: `TextCell`, `AgeCell`, `CountCell`, `KnownCount`
  (renders `-` when unknown), `ModeCell` (the normalised word only),
  `AliasCell` (short kind, full in details), `BoolCell`. Ages render
  against the renderer's clock, so engines never call `time.Now` and goldens
  stay deterministic.
- **Row actions**: `Drill` is where `enter` goes (e.g. `{Command: "v",
  Filter: …}` for a policy's violations), `Detail` backs `d` (sections take
  a `Language` for highlighting), `Ref` backs `g`, `Object` backs `y`/`e`,
  and `Policy` (the row's policy key) enables `i` and the impact view.
  `NoObject` explains why `y` has nothing to show.
- **Keys** are view-specific single uppercase keys that change view state
  only, never the cluster. They take precedence over a shared key they
  shadow in that view (`vap/pol`'s `G`), so pick one deliberately.

## Optional interfaces

Implement these when the engine has the information; shared features light
up on their own.

| Interface | Gives | Example |
|---|---|---|
| `engine.KindNamer` | `ShortKind(kind)`: short kinds in tables | Kyverno `ClusterPolicy` → `CPol`, `vap` → `VAP` |
| `engine.Auditor` | `AuditInterval()`: the header grades audit staleness against it (0 = audit disabled) | read from the controller's flags, else the engine's default |
| `engine.Platform` | `PlatformInfo()`: namespace, webhook label selector, audit Lease and source, so `:health` finds the webhooks, controllers and leader | Gatekeeper, Kyverno |
| `engine.PolicyStatuser` | `PolicyStatus()`: the `:health` Policies row (loaded / total, not enforced, compile errors, missing CRDs, not ready) | all three |
| `engine.EventSource` | `EventSelectors()`, `ParseEvent()`, `EventsEnabled()`: admission denials in the recent tab and the overview | Gatekeeper `FailedAdmission`, Kyverno `PolicyViolation` |
| `engine.Generator` | `GeneratedFrom(policy)`: objects generated from another engine's policy, listed in that policy's `:pol` detail | `vap` names the VAPs Gatekeeper and Kyverno generate |

Verify every marker you rely on (event reasons and sources, annotations,
owner references, flag names) on a real cluster before coding it, and write
the verified shape into fixtures and a comment. Field layouts change between
releases.

## Registering

```go
package vap

func init() { engine.Register(Name, New) }

func New(c engine.Cluster) engine.Engine { … }
```

Add a blank import in `cmd/p9s/app/root.go`:

```go
_ "github.com/yindia/p9s/internal/engines/vap"
```

`--engine auto` (the default) runs `Detect` for every registered engine;
`--engine gatekeeper,vap` selects some.

## Testing

1. **Contract suite**: the minimum bar. It checks identity, detection,
   capabilities, well-formed policies and violations, view roles and
   drill-downs, that rows drilling into a policy carry its key, the
   optional interfaces, concurrent reads, sync and an idempotent `Stop`.

   ```go
   func TestContract(t *testing.T) {
       var cl *fakecluster.Cluster
       enginetest.Run(t, enginetest.Harness{
           New: func(t *testing.T) engine.Engine {
               cl = vaptest.NewCluster(t, vaptest.Options{})
               return vap.New(cl)
           },
           Informers: func(*testing.T) dynamicinformer.DynamicSharedInformerFactory {
               return dynamicinformer.NewDynamicSharedInformerFactory(cl.Dyn, 0)
           },
           NotInstalled: func(t *testing.T) engine.Engine {
               return vap.New(vaptest.NewCluster(t, vaptest.Options{NoVAP: true}))
           },
           Events: nil, // sample admission Events, required for an EventSource
       })
   }
   ```

2. **A fixture package** (`<name>test`) with YAML copied from a real
   cluster and a `NewCluster(t, Options)` built on `fakecluster.New`, which
   seeds objects under their served resource (the raw fake tracker guesses
   plurals like `k8srequiredlabelses`). Options for "not installed", "no
   reports", `Forbidden` and `ClusterWideForbidden` resources make the RBAC
   cases one-liners.
3. **Unit tests** for normalisation, health, RBAC (denied resources, the
   namespace fallback) and anything run in the background.
4. **Goldens** in `test/golden`: render your views through a real session
   and the UI (`make golden` regenerates them). Wait for the synced header
   before capturing, or the golden records `syncing…`.
5. **e2e** against kind: install the engine in `hack/e2e-setup.sh` (plus
   anything your tests need under `hack/manifests/`) and add a section to
   `test/e2e/e2e_test.go`.
6. **Snapshots** work for free when every read goes through the cluster's
   dynamic, metadata and discovery clients: `test/golden/snapshot_test.go`
   checks that a recorded cluster replays to identical views.

## Checklist

- [ ] `Detect` from discovery; version from the controller image or `"unknown"`
- [ ] every informer through `Access.SharedInformer` (or with the transform,
      watch error handler and `Track` done by hand); nothing polls
- [ ] `Sync` started, tracked, gated for background work, and `Watch`ed
- [ ] reads are cached snapshots and never call the API
- [ ] enforcement normalised, raw term kept; unknown counts are `-`, not 0
- [ ] truncated lists flagged; RBAC denials reported, never fatal
- [ ] views: prefixed commands, roles, `DefaultSort`, `Row.Policy` on policy rows
- [ ] markers verified on a real cluster and recorded in fixtures
- [ ] contract suite, fixtures, unit tests, goldens, e2e
- [ ] Docs: the engine in the README's Supported engines table, and its detection and views in [engines.md](engines.md)
