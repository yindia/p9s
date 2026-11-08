package ui

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"

	"github.com/yindia/p9s/internal/views"
)

const (
	defaultTick = time.Second
	// flashTTL is how long a footer message stays before the hints return.
	flashTTL       = 4 * time.Second
	getTimeout     = 10 * time.Second
	connectTimeout = 45 * time.Second
	chromeLines    = 3 // header, title bar, footer
)

type inputMode int

const (
	inputNone inputMode = iota
	inputCommand
	inputFilter
)

// screen is either a *tableScreen or a *detailScreen.
type screen interface{ title() string }

// App is the root Bubble Tea model.
type App struct {
	backend Backend
	env     views.Env
	opts    Options
	st      styles
	hl      highlighter

	width, height int
	stack         []screen
	mode          inputMode
	input         textinput.Model
	flash         string
	flashErr      bool
	flashAt       time.Time
	// help is the open "?" page, or nil.
	help *detailScreen

	// namespace is the ":ns" scope applied to every view; "" means all.
	namespace string
	nsList    []string
	nsListErr error

	connecting   bool
	connectingTo string
	connErr      error
	listening    bool
	// gen increments on every successful connect; async results from an
	// older generation (another cluster) are dropped.
	gen int
}

// New builds the root model.
func New(b Backend, opts Options) *App {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.InitialCommand == "" {
		opts.InitialCommand = views.CmdOverview
	}
	if opts.TickInterval == 0 {
		opts.TickInterval = defaultTick
	}
	if opts.Copy == nil {
		opts.Copy = osc52Copy
	}
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 256
	a := &App{
		backend: b, opts: opts, st: newStyles(),
		hl:    highlighter{enabled: !opts.NoHighlight},
		input: in, width: 100, height: 30, connecting: true,
	}
	a.env = views.Env{
		Engines: b.Engines, Detections: b.Detections, ContextName: b.ContextName, Contexts: b.Contexts,
		ConnErr:    func() error { return a.connErr },
		Namespaces: func() ([]string, error) { return a.nsList, a.nsListErr },
		Scope:      func() string { return a.namespace },
		Now:        a.now,
		Owners:     b.Owners,
		Platform:   b.Platform,
		Events:     b.Events,
		UpdatedAt:  b.UpdatedAt,
		GroupLabel: func() string { return a.opts.GroupLabel },
	}
	return a
}

func (a *App) now() time.Time { return a.opts.Now() }

// Messages.
type (
	connectedMsg struct{ err error }
	updateMsg    struct{}
	tickMsg      struct{}
	objectMsg    struct {
		gen   int
		title string
		obj   objectResult
	}
	editorDoneMsg struct{ err error }
	namespacesMsg struct {
		gen   int
		names []string
		err   error
	}
)

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	return tea.Batch(a.connectCmd(""), a.tickCmd())
}

func (a *App) connectCmd(name string) tea.Cmd {
	b := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		if name == "" {
			return connectedMsg{err: b.Connect(ctx)}
		}
		return connectedMsg{err: b.SwitchContext(ctx, name)}
	}
}

func (a *App) tickCmd() tea.Cmd {
	if a.opts.TickInterval < 0 {
		return nil
	}
	return tea.Tick(a.opts.TickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (a *App) waitUpdate() tea.Cmd {
	ch := a.backend.Updates()
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return updateMsg{}
	}
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil
	case connectedMsg:
		return a, a.onConnected(msg.err)
	case updateMsg:
		a.refreshTop()
		return a, a.waitUpdate()
	case tickMsg:
		if a.flash != "" && a.now().Sub(a.flashAt) >= flashTTL {
			a.flash = ""
		}
		return a, a.tickCmd()
	case objectMsg:
		if msg.gen != a.gen {
			return a, nil // result from a previous context
		}
		if msg.obj.err != nil {
			a.setFlash(msg.obj.err.Error(), true)
			return a, nil
		}
		a.flash = ""
		a.push(newYAMLScreen(msg.title, msg.obj.obj))
		return a, nil
	case namespacesMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		a.nsList, a.nsListErr = msg.names, msg.err
		if msg.err != nil {
			a.setFlash("cannot list namespaces ("+msg.err.Error()+"); showing namespaces seen in violations", true)
		}
		a.refreshTop()
		return a, nil
	case editorDoneMsg:
		a.onEditorDone(msg.err)
		return a, nil
	case tea.KeyMsg:
		return a, a.onKey(msg)
	}
	return a, nil
}

func (a *App) onConnected(err error) tea.Cmd {
	a.connecting, a.connectingTo = false, ""
	if err != nil {
		a.setFlash("connect failed: "+err.Error(), true)
		if a.backend.ContextName() != "" {
			return nil // a failed switch keeps the live connection and view
		}
		// Never connected: :ctx still works without a connection.
		a.connErr = err
		a.stack = nil
		a.runCommand(views.CmdContexts)
		return nil
	}
	a.connErr = nil
	a.gen++
	a.stack = nil
	// Namespaces differ per cluster; never carry a scope across contexts.
	a.namespace, a.nsList, a.nsListErr = "", nil, nil
	a.setFlash("connected to "+a.backend.ContextName(), false)
	a.runCommand(a.opts.InitialCommand)
	if a.listening {
		return nil
	}
	a.listening = true
	return a.waitUpdate()
}

func (a *App) top() screen {
	if len(a.stack) == 0 {
		return nil
	}
	return a.stack[len(a.stack)-1]
}

func (a *App) push(s screen) {
	if t, ok := s.(*tableScreen); ok {
		t.refresh()
	}
	a.stack = append(a.stack, s)
}

func (a *App) pop() {
	if len(a.stack) <= 1 {
		return
	}
	a.stack = a.stack[:len(a.stack)-1]
	a.refreshTop()
}

func (a *App) refreshTop() {
	switch s := a.top().(type) {
	case *tableScreen:
		s.refresh()
	case *overviewScreen:
		s.refresh()
	case *healthScreen:
		s.refresh()
	}
}

func (a *App) setFlash(s string, isErr bool) {
	a.flash, a.flashErr, a.flashAt = s, isErr, a.now()
}

func (a *App) openHelp() {
	a.help = newDetailScreen(helpDetail(a.env), nil)
}

// osc52Copy writes the OSC 52 clipboard sequence to the terminal: stderr
// when it is one (it never interleaves with the renderer's stdout writes),
// else /dev/tty. With neither it fails instead of claiming success, e.g.
// when stderr is redirected to a file.
func osc52Copy(text string) error {
	if term.IsTerminal(os.Stderr.Fd()) {
		termenv.NewOutput(os.Stderr).Copy(text)
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return errors.New("no terminal to copy to (stderr is redirected)")
	}
	defer tty.Close()
	termenv.NewOutput(tty).Copy(text)
	return nil
}
