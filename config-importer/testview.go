package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Be1zebub/sing-box-tray/config-importer/internal/singbox"
	"github.com/Be1zebub/sing-box-tray/config-importer/internal/testrun"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	failStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

type testMsg struct {
	res  testrun.Result
	done bool
	err  error
}

type testModel struct {
	outbounds []singbox.Obj
	opts      testrun.Options
	ctx       context.Context
	cancel    context.CancelFunc

	spinner spinner.Model
	results []testrun.Result
	ch      chan testMsg
	done    bool
	err     error
	quit    bool
}

func newTestModel(outbounds []singbox.Obj, opts testrun.Options) *testModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	ctx, cancel := context.WithCancel(context.Background())
	return &testModel{
		outbounds: outbounds,
		opts:      opts,
		ctx:       ctx,
		cancel:    cancel,
		spinner:   sp,
	}
}

func (m *testModel) Init() tea.Cmd {
	ch := make(chan testMsg)
	go func() {
		_, err := testrun.Run(m.ctx, m.outbounds, m.opts, func(r testrun.Result) {
			ch <- testMsg{res: r}
		})
		ch <- testMsg{done: true, err: err}
		close(ch)
	}()
	m.ch = ch
	return tea.Batch(m.spinner.Tick, waitTestMsg(ch))
}

func waitTestMsg(ch <-chan testMsg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m *testModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.cancel()
			m.quit = true
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case testMsg:
		if msg.done {
			m.done = true
			m.err = msg.err
			return m, tea.Quit
		}
		m.results = append(m.results, msg.res)
		return m, waitTestMsg(m.ch)
	}
	return m, nil
}

func (m *testModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Testing outbounds (SOCKS only, system route untouched)") + "\n\n")

	for _, r := range m.results {
		if r.OK {
			fmt.Fprintf(&b, "  %s %-40s %5dms  %8s/s  %s\n",
				okStyle.Render("OK  "), r.Tag, r.Latency.Milliseconds(),
				humanBytes(int64(r.Speed)), humanBytes(r.Bytes))
			continue
		}
		fmt.Fprintf(&b, "  %s %-40s %s\n", failStyle.Render("FAIL"), r.Tag, r.Err)
	}

	if !m.done && !m.quit {
		fmt.Fprintf(&b, "\n  %s waiting for results...", m.spinner.View())
	} else if m.err != nil {
		fmt.Fprintf(&b, "\n  %s\n", failStyle.Render("test run error: "+m.err.Error()))
	}
	return b.String()
}

// runTestView tests outbounds and returns the results.
func runTestView(outbounds []singbox.Obj, opts testrun.Options) ([]testrun.Result, error) {
	m := newTestModel(outbounds, opts)
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		return m.results, err
	}
	if m.err != nil {
		return m.results, m.err
	}
	time.Sleep(200 * time.Millisecond)
	return m.results, nil
}

// pauseModel quits on any key press.
type pauseModel struct{}

func (pauseModel) Init() tea.Cmd { return nil }

func (pauseModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return pauseModel{}, tea.Quit
	}
	return pauseModel{}, nil
}

func (pauseModel) View() string { return "" }

// pauseWithMessage prints text and waits for a key press, so the terminal does
// not look like it crashed on exit. Falls back to Enter when not a TTY.
func pauseWithMessage(text string) {
	fmt.Println()
	fmt.Println(dimStyle.Render(text))
	if _, err := tea.NewProgram(pauseModel{}).Run(); err != nil {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
}
