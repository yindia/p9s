package render

// htmlTemplate is the self-contained report page. Colours are tokens with a
// dark variant; no scripts, fonts or images are loaded.
const htmlTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>p9s policy report</title>
<style>
:root {
  --bg: #fbfaf7; --fg: #1d1f24; --muted: #5f6470; --line: #dcd9d1; --panel: #ffffff;
  --crit: #b3261e; --warn: #8a5a00; --ok: #1e6b3a; --accent: #2f4a8a;
  --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  --sans: system-ui, -apple-system, "Segoe UI", sans-serif;
}
@media (prefers-color-scheme: dark) {
  :root { --bg: #15171b; --fg: #e6e4df; --muted: #9aa0ab; --line: #30333a; --panel: #1c1f24;
    --crit: #ff8a80; --warn: #f2c14e; --ok: #7bd389; --accent: #9fb4ff; }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 15px/1.5 var(--sans); }
main { max-width: 1100px; margin: 0 auto; padding: 32px 16px 64px; }
h1 { font-size: 28px; margin: 0 0 4px; letter-spacing: -0.01em; }
h2 { font-size: 18px; margin: 40px 0 12px; padding-top: 16px; border-top: 1px solid var(--line); }
.meta { display: grid; grid-template-columns: max-content 1fr; gap: 4px 16px; color: var(--muted); margin: 12px 0 24px; }
.meta b { color: var(--fg); font-weight: 600; }
code, .mono { font-family: var(--mono); font-size: 13px; word-break: break-all; }
.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; }
.tile { background: var(--panel); border: 1px solid var(--line); border-radius: 6px; padding: 12px 14px; }
.tile .n { font-size: 26px; font-weight: 650; font-variant-numeric: tabular-nums; }
.tile .l { color: var(--muted); font-size: 13px; }
table { width: 100%; border-collapse: collapse; background: var(--panel); border: 1px solid var(--line); margin: 8px 0 16px; }
th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid var(--line); vertical-align: top; }
th { font-size: 12px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); font-weight: 600; }
td.n { text-align: right; font-variant-numeric: tabular-nums; }
.critical, .missing { color: var(--crit); font-weight: 600; }
.warning, .audit { color: var(--warn); font-weight: 600; }
.enforced { color: var(--ok); font-weight: 600; }
.note { color: var(--muted); font-size: 14px; }
.notice { border-left: 3px solid var(--warn); padding: 4px 12px; margin: 8px 0; }
.scroll { overflow-x: auto; }
hr { border: 0; border-top: 2px solid var(--line); margin: 56px 0; }
</style>
</head>
<body>
<main>
{{range $i, $r := .Reports}}{{if $i}}<hr>{{end}}
<h1>Policy report: {{$r.Header.Context}}</h1>
{{if $r.Error}}<p class="notice"><b>This context could not be reported on:</b> {{$r.Error}}</p>{{else}}
<div class="meta">
  <span>Cluster</span><b>{{dash $r.Header.Cluster}}</b>
  <span>Engines</span><b>{{engines $r.Header}}</b>
  <span>Generated</span><b>{{time $r.Header.GeneratedAt}}</b>
  {{with $r.Header.Snapshot}}<span>Snapshot</span><b>{{time .}}</b>{{end}}
  <span>p9s</span><b>{{$r.Header.P9sVersion}}</b>
  <span>Body SHA-256</span><code>{{$r.Header.BodySHA256}}</code>
</div>
{{range $r.Notices}}<p class="notice">{{.}}</p>{{end}}

<h2>1. Summary</h2>
<div class="tiles">
  <div class="tile"><div class="n">{{$r.Summary.Violations}}</div><div class="l">violations (engines' own counts){{if $r.Summary.UnknownPolicies}}; {{$r.Summary.UnknownPolicies}} policies without audit data{{end}}</div></div>
  <div class="tile"><div class="n critical">{{$r.Summary.Health.Critical}}</div><div class="l">critical health findings</div></div>
  <div class="tile"><div class="n warning">{{$r.Summary.Health.Warning}}</div><div class="l">warnings</div></div>
  <div class="tile"><div class="n">{{len $r.Policies}}</div><div class="l">policies: {{modes $r.Summary.PoliciesByMode}}</div></div>
</div>

<h2>2. Pod Security Standards coverage</h2>
<p class="note">Mapped from policy metadata only. Namespace exclusions are not considered.</p>
<div class="scroll"><table>
<tr><th>Level</th><th>Control</th><th>Status</th><th>Caveats</th><th>Policies</th></tr>
{{range $r.PSS.Controls}}<tr><td>{{.Level}}</td><td>{{.Name}}</td><td class="{{if eq .Status "enforced"}}enforced{{else if eq .Status "missing"}}missing{{else}}audit{{end}}">{{.Status}}</td><td class="note">{{range $j, $c := .Caveats}}{{if $j}}; {{end}}{{$c}}{{end}}</td><td class="mono">{{keys .Policies}}</td></tr>
{{end}}</table></div>
<p class="note">Uncategorised ({{len $r.PSS.Uncategorised}}): <span class="mono">{{keys $r.PSS.Uncategorised}}</span></p>

<h2>3. Violations</h2>
{{if $r.Violations.Truncated}}<p class="notice">Some engines list fewer violations than they count: breakdowns cover the listed ones only.</p>{{end}}
<div class="tiles">
{{range $t := (index $.Tables $i)}}<div class="scroll"><table><tr><th>{{$t.Title}}</th><th>Count</th></tr>
{{range $t.Rows}}<tr><td class="mono">{{.Name}}</td><td class="n">{{.Count}}</td></tr>{{else}}<tr><td>-</td><td class="n">0</td></tr>{{end}}
</table></div>{{end}}
</div>

<h2>4. Fail-open and other health findings</h2>
{{if $r.Findings}}<div class="scroll"><table><tr><th>Severity</th><th>Panel</th><th>Engine</th><th>Check</th><th>Status</th></tr>
{{range $r.Findings}}<tr><td class="{{.Severity}}">{{.Severity}}</td><td>{{.Panel}}</td><td>{{.Engine}}</td><td class="mono">{{.Check}}</td><td>{{.Status}}</td></tr>
{{end}}</table></div>{{else}}<p>No critical or warning findings.</p>{{end}}

<h2>5. Unprotected kinds <span class="note">(approximate)</span></h2>
<p class="note">Served, user-created kinds no active validating policy selects by kind. Namespace selectors, exclusions and conditions are not read.</p>
{{with $r.Unprotected.Error}}<p class="notice">{{.}}</p>{{end}}
{{if $r.Unprotected.AllKinds}}<p class="note">Not counted (no kind restriction, so they select every kind but usually check one): <span class="mono">{{keys $r.Unprotected.AllKinds}}</span></p>{{end}}
{{if $r.Unprotected.Kinds}}<div class="scroll"><table><tr><th>Kind</th><th>Group</th><th>Scope</th><th>Note</th></tr>
{{range $r.Unprotected.Kinds}}<tr><td>{{.Kind}}</td><td class="mono">{{dash .Group}}</td><td>{{if .Namespaced}}namespaced{{else}}cluster{{end}}</td><td class="note">{{if .ViaPods}}its Pods are checked by Pod policies{{end}}</td></tr>
{{end}}</table></div>{{else if not $r.Unprotected.Error}}<p>Every served kind is selected by some policy.</p>{{end}}

{{with $r.Since}}<h2>6. Changes since {{time .Previous}}</h2>
{{if not .Comparable}}<p class="notice">One of the reports had capped violation lists: some changes may only reflect the cap.</p>{{end}}
<div class="tiles">
  <div class="tile"><div class="n">{{len .NewViolations}}</div><div class="l">new violations</div></div>
  <div class="tile"><div class="n">{{len .ResolvedViolations}}</div><div class="l">resolved violations</div></div>
  <div class="tile"><div class="n">{{len .ChangedMessages}}</div><div class="l">still failing, new message</div></div>
  <div class="tile"><div class="n">{{len .EnforcementChanged}}</div><div class="l">enforcement changes</div></div>
  <div class="tile"><div class="n">{{len .AddedPolicies}} / {{len .RemovedPolicies}}</div><div class="l">policies added / removed</div></div>
</div>
{{if .EnforcementChanged}}<table><tr><th>Policy</th><th>From</th><th>To</th></tr>{{range .EnforcementChanged}}<tr><td class="mono">{{.Policy}}</td><td>{{.From}}</td><td>{{.To}}</td></tr>{{end}}</table>{{end}}
{{end}}
<h2>Not included</h2>
<ul>{{range $r.Omitted}}<li class="note">{{.}}</li>{{end}}</ul>
{{end}}
{{end}}
</main>
</body>
</html>
`
