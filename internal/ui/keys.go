package ui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/table"
	"github.com/yindia/p9s/internal/views"
)

type objectResult struct {
	obj *unstructured.Unstructured
	err error
}

func (a *App) onKey(k tea.KeyMsg) tea.Cmd {
	if k.Type == tea.KeyCtrlC {
		return tea.Quit
	}
	if a.mode != inputNone {
		return a.onInputKey(k)
	}
	if a.help != nil {
		switch k.String() {
		case "?", "esc", "q":
			a.help = nil
		default:
			a.onDetailKey(a.help, k)
		}
		return nil
	}
	switch k.String() {
	case ":":
		a.openInput(inputCommand, "")
		return nil
	case "/":
		if t, ok := a.top().(*tableScreen); ok {
			a.openInput(inputFilter, t.text)
		}
		return nil
	case "?":
		a.openHelp()
		return nil
	case "0":
		if a.namespace != "" {
			return a.setNamespace("")
		}
		return nil
	case "esc":
		// esc clears a filter the user typed; a preset filter is part of
		// where they came from, so esc goes straight back.
		if t, ok := a.top().(*tableScreen); ok && t.text != "" && t.text != t.preset {
			t.setText(t.preset)
			return nil
		}
		a.pop()
		return nil
	}
	a.flash = ""
	switch s := a.top().(type) {
	case *tableScreen:
		return a.onTableKey(s, k)
	case *detailScreen:
		return a.onDetailKey(s, k)
	case *overviewScreen:
		return a.onOverviewKey(s, k)
	case *healthScreen:
		return a.onHealthKey(s, k)
	}
	return nil
}

func (a *App) onHealthKey(h *healthScreen, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "tab":
		h.switchPanel(1)
		return nil
	case "shift+tab":
		h.switchPanel(-1)
		return nil
	case "N", "V", "A", "S", "o", "[", "]":
		return nil // panels have a fixed order: worst first
	}
	if p := h.panel(); p != nil {
		return a.onTableKey(p, k)
	}
	return nil
}

func (a *App) onOverviewKey(o *overviewScreen, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "up", "k", "shift+tab":
		o.move(-1)
	case "down", "j", "tab":
		o.move(1)
	case "home":
		o.selected = 0
	case "end", "G":
		o.move(len(o.ov.Blocks))
	case "enter":
		b, ok := o.current()
		if !ok || b.Target == nil {
			a.setFlash("nothing to open here yet", true)
			return nil
		}
		if b.Target.Command == views.CmdHealth {
			a.push(newHealthScreen(a.env))
			return nil
		}
		spec, f, err := views.Resolve(a.env, b.Target.Command, "")
		if err != nil {
			a.setFlash(err.Error(), true)
			return nil
		}
		f.Text = b.Target.Filter
		a.push(a.newScreen(spec, f))
	}
	return nil
}

func (a *App) openInput(m inputMode, value string) {
	a.mode = m
	a.input.SetValue(value)
	a.input.CursorEnd()
	a.input.Focus()
}

func (a *App) closeInput() {
	a.mode = inputNone
	a.input.Blur()
}

func (a *App) onInputKey(k tea.KeyMsg) tea.Cmd {
	switch k.Type {
	case tea.KeyEsc:
		if t, ok := a.top().(*tableScreen); ok && a.mode == inputFilter {
			t.setText("")
		}
		a.closeInput()
		return nil
	case tea.KeyEnter:
		value, mode := a.input.Value(), a.mode
		a.closeInput()
		if mode == inputCommand {
			// A message about the view being left (e.g. "flat list") must
			// not follow the reader into the next; the command may set its own.
			a.flash = ""
			return a.runCommand(value)
		}
		if t, ok := a.top().(*tableScreen); ok && mode == inputFilter {
			// "No rows match" alone hides a typo such as sev:hgih.
			problems, _ := t.tbl.CheckFilter(value)
			if len(problems) > 0 {
				a.setFlash(problems[0], true)
			}
		}
		return nil
	case tea.KeyTab:
		if a.mode == inputCommand {
			a.input.SetValue(views.Complete(a.env, a.input.Value()))
			a.input.CursorEnd()
		}
		return nil
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(k)
	if t, ok := a.top().(*tableScreen); ok && a.mode == inputFilter {
		t.setText(a.input.Value())
	}
	return cmd
}

func (a *App) onTableKey(t *tableScreen, k tea.KeyMsg) tea.Cmd {
	bodyH := max(a.height-chromeLines-1, 1)
	// A view's own keys win over the shared ones it shadows (vap/pol's G;
	// end still jumps to the last row there).
	if a.viewKey(t, k.String()) {
		return nil
	}
	switch k.String() {
	case "up", "k":
		t.move(-1)
	case "down", "j":
		t.move(1)
	case "pgup", "ctrl+b":
		t.move(-bodyH)
	case "pgdown", "ctrl+f", " ":
		t.move(bodyH)
	case "home":
		t.moveTo(0)
	case "end", "G":
		t.moveTo(len(t.visible) - 1)
	case "enter":
		return a.enter(t)
	case "d":
		return a.describe(t)
	case "y":
		return a.showYAML(t)
	case "e":
		row, ok := t.current()
		if !ok || row.Source.Object == nil {
			msg := "nothing to edit on this row"
			if ok && row.Source.NoObject != "" {
				msg = row.Source.NoObject
			}
			a.setFlash(msg, true)
			return nil
		}
		return a.openEditor(table.ObjectYAML(row.Source.Object))
	case "g":
		return a.jump(t)
	case "c", "C":
		return a.copyTarget(t, k.String() == "C")
	case "N", "V", "A", "S":
		a.sortBy(t, sortKeys[k.String()])
	case "o":
		a.toggleGrouping(t)
	case "[", "]":
		a.switchViolationsTab(t, k.String() == "]")
	case "i":
		a.openImpact(t)
	}
	return nil
}

// viewKey runs a view-specific key (engine.View.Keys) and reports whether
// the view had one.
func (a *App) viewKey(t *tableScreen, key string) bool {
	for _, vk := range t.spec.View.Keys {
		if vk.Key == key && vk.Do != nil {
			msg := vk.Do()
			t.refresh()
			a.setFlash(msg, false)
			return true
		}
	}
	return false
}

// openImpact shows what enforcing the selected policy would block.
func (a *App) openImpact(t *tableScreen) {
	row, ok := t.current()
	if !ok || row.Policy == nil {
		a.setFlash("i works on policy and violation rows", true)
		return
	}
	a.push(a.newScreen(views.ImpactSpec(a.env, *row.Policy), engine.Filter{}))
}

// switchViolationsTab moves :v between the current tab (audit results, in
// the grouping last used there) and the recent tab (admission events).
func (a *App) switchViolationsTab(t *tableScreen, recent bool) {
	switch {
	case !views.IsViolations(t.spec) && !views.IsRecent(t.spec):
		a.setFlash("[ ] switch the tabs of :v", true)
		return
	case recent == views.IsRecent(t.spec):
		return
	case recent:
		t.vGrouping = views.GroupingOf(t.spec)
		t.spec = a.opts.Sorts.WithSort(views.RecentSpec(a.env))
		a.setFlash("recent: admission events", false)
	default:
		t.spec = a.opts.Sorts.WithSort(views.ViolationsSpec(a.env, t.vGrouping))
		a.setFlash("current: audit results", false)
	}
	t.filter.Owner = ""
	t.userSort = nil
	t.refresh()
}

// toggleGrouping switches :v between grouped by owner and flat (o). The
// policy filter is kept.
func (a *App) toggleGrouping(t *tableScreen) {
	if views.IsRecent(t.spec) {
		a.setFlash("grouping applies to the current tab of :v", true)
		return
	}
	if !views.IsViolations(t.spec) {
		a.setFlash("grouping applies to :v", true)
		return
	}
	next, name := views.Flat, "flat list"
	if views.GroupingOf(t.spec) == views.Flat {
		next, name = views.GroupByOwner, "grouped by owner"
	}
	t.spec = a.opts.Sorts.WithSort(views.ViolationsSpec(a.env, next))
	t.filter.Owner = "" // a group expansion does not survive a regroup
	t.userSort = nil
	t.refresh()
	a.setFlash(name, false)
}

// sortKeys maps shift+letter to the column role it sorts by, with the
// direction used on first press.
var sortKeys = map[string]struct {
	role engine.Role
	desc bool
	name string
}{
	"N": {engine.RoleName, false, "name"},
	"V": {engine.RoleCount, true, "violations"},
	"A": {engine.RoleAge, false, "age"},
	"S": {engine.RoleSeverity, true, "severity"},
}

func (a *App) sortBy(t *tableScreen, key struct {
	role engine.Role
	desc bool
	name string
}) {
	col := t.tbl.ColumnFor(key.role)
	if col < 0 {
		a.setFlash("this view has no "+key.name+" column", true)
		return
	}
	desc := key.desc
	if t.userSort != nil && t.userSort.Column == col {
		desc = !t.userSort.Desc // the same key pressed again reverses
	}
	t.setSort(col, desc)
}

// copyTarget copies the row's object as kind/ns/name, or as a kubectl
// command, to the terminal clipboard (OSC 52, so it works over SSH).
func (a *App) copyTarget(t *tableScreen, asCommand bool) tea.Cmd {
	row, ok := t.current()
	if !ok || row.Target == nil || row.Target.Name == "" {
		a.setFlash("this row does not point at an object", true)
		return nil
	}
	text := views.CopyRef(*row.Target)
	if asCommand {
		text = views.KubectlGet(*row.Target, a.backend.ContextName())
	}
	if err := a.opts.Copy(text); err != nil {
		a.setFlash("copy failed: "+err.Error(), true)
		return nil
	}
	a.setFlash("copied: "+text, false)
	return nil
}

func (a *App) onDetailKey(d *detailScreen, k tea.KeyMsg) tea.Cmd {
	bodyH := max(a.height-chromeLines, 1)
	switch k.String() {
	case "up", "k":
		d.scroll(-1)
	case "down", "j":
		d.scroll(1)
	case "pgup", "ctrl+b":
		d.scroll(-bodyH)
	case "pgdown", "ctrl+f", " ":
		d.scroll(bodyH)
	case "home":
		d.top()
	case "end", "G":
		d.bottom()
	case "q":
		a.pop()
	case "y":
		if d.object != nil {
			a.push(newYAMLScreen(d.heading+" (YAML)", d.object))
		}
	case "e":
		return a.openEditor(d.text())
	}
	return nil
}

func (a *App) enter(t *tableScreen) tea.Cmd {
	row, ok := t.current()
	if !ok {
		return nil
	}
	if t.onEnter != nil {
		return t.onEnter(row)
	}
	if row.Source.Drill != nil {
		spec, f, err := views.Target(a.env, *row.Source.Drill)
		if err != nil {
			a.setFlash(err.Error(), true)
			return nil
		}
		a.push(a.newScreen(spec, f))
		return nil
	}
	return a.describe(t)
}

func (a *App) describe(t *tableScreen) tea.Cmd {
	row, ok := t.current()
	if !ok {
		return nil
	}
	if row.Source.Detail == nil {
		return a.showYAML(t)
	}
	d, err := row.Source.Detail()
	if err != nil {
		a.setFlash(err.Error(), true)
		return nil
	}
	a.push(newDetailScreen(table.BuildDetail(d), row.Source.Object))
	return nil
}

func (a *App) showYAML(t *tableScreen) tea.Cmd {
	row, ok := t.current()
	if !ok {
		return nil
	}
	obj := row.Source.Object
	if obj == nil {
		msg := "no YAML for this row"
		if row.Source.NoObject != "" {
			msg = row.Source.NoObject
		}
		a.setFlash(msg, true)
		return nil
	}
	a.push(newYAMLScreen(obj.GetKind()+" "+obj.GetName(), obj))
	return nil
}

// jump fetches the object a row points at (a violation's target).
func (a *App) jump(t *tableScreen) tea.Cmd {
	row, ok := t.current()
	if !ok {
		return nil
	}
	if row.Target == nil || row.Target.Name == "" {
		a.setFlash("this row does not point at an object", true)
		return nil
	}
	ref, b, gen := *row.Target, a.backend, a.gen
	a.setFlash("loading "+ref.String()+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), getTimeout)
		defer cancel()
		obj, err := b.GetObject(ctx, ref)
		return objectMsg{gen: gen, title: ref.String(), obj: objectResult{obj: obj, err: views.JumpError(ref, err)}}
	}
}

func (a *App) switchContext(row table.Row) tea.Cmd {
	if a.connecting {
		a.setFlash("already switching to "+a.connectingTo, true)
		return nil
	}
	a.connecting, a.connectingTo = true, row.ID
	a.setFlash("switching to "+row.ID+"…", false)
	return a.connectCmd(row.ID)
}

// openEditor shows content in $EDITOR. Edits are never applied while the
// session is read-only; applying them is a phase 5 write action.
func (a *App) openEditor(content string) tea.Cmd {
	editor := a.opts.Editor
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if editor == "" {
			editor = os.Getenv(env)
		}
	}
	if editor == "" {
		editor = "vi"
	}
	f, err := os.CreateTemp("", "p9s-*.yaml")
	if err != nil {
		a.setFlash("editor: "+err.Error(), true)
		return nil
	}
	path := f.Name()
	_, werr := f.WriteString(content + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		a.setFlash("editor: "+werr.Error(), true)
		return nil
	}
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], path)...) //nolint:gosec // the user's own $EDITOR
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		_ = os.Remove(path)
		return editorDoneMsg{err: err}
	})
}

func (a *App) onEditorDone(err error) {
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		a.setFlash("editor exited with "+exitErr.String(), true)
	case err != nil:
		a.setFlash("editor: "+err.Error(), true)
	case a.backend.ReadOnly():
		a.setFlash("read-only mode: changes were not applied", false)
	default:
		a.setFlash("applying edits is not available yet; changes were not applied", false)
	}
}
