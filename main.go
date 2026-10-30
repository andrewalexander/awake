// awake wraps `caffeinate -dimsu -t <secs>` (macOS) or
// `systemd-inhibit --what=idle:sleep sleep <secs>` (Linux) with a small TUI.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tickMsg time.Time
type doneMsg struct{ err error }

type model struct {
	total    time.Duration
	start    time.Time
	elapsed  time.Duration
	spinner  spinner.Model
	bar      progress.Model
	titleMsg string
	finished bool
	err      error
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tea.Batch(m.spinner.Tick, tick()) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	case tickMsg:
		m.elapsed = min(time.Since(m.start), m.total)
		return m, tick()
	case doneMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.elapsed = m.total
		m.finished = true
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.bar.Width = min(msg.Width-4, 50)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	h, mnt, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, mnt, s)
	}
	return fmt.Sprintf("%02dm%02ds", mnt, s)
}

func (m model) View() string {
	if m.err != nil {
		return ""
	}
	pct := float64(m.elapsed) / float64(m.total)
	head := m.spinner.View() + " " + titleStyle.Render(m.titleMsg)
	if m.finished {
		// cannot convert time.Now().Local() (value of struct type "time".Time)
		// to type string
		ts := fmt.Sprintf("%s", time.Now().Local())
		head = "✓ " + titleStyle.Render("done at", ts)
	}
	return fmt.Sprintf("\n  \n%s\n  awake for %s / %s  (%.0f%%)\n  %s\n  %s\n",
		head, fmtDur(m.elapsed), fmtDur(m.total), pct*100,
		m.bar.ViewAs(pct), dimStyle.Render("q to stop"))
}

// inhibitCmd returns a command that keeps the machine awake for d and then exits.
func inhibitCmd(d time.Duration, why string) *exec.Cmd {
	secs := strconv.Itoa(int(d.Seconds()))
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("caffeinate", "-dimsu", "-t", secs)
	case "linux":
		return exec.Command("systemd-inhibit", "--what=idle:sleep", "--who=awake", "--why="+why,
			"--mode=block", "sleep", secs)
	}
	return nil
}

func main() {
	defaultMsg := "keeping the machine awake"
	if runtime.GOOS == "darwin" {
		defaultMsg = "keeping the mac awake"
	}
	total := flag.Duration("t", 30*time.Minute, "how long to stay awake (e.g. 30m, 2h)")
	titleMsg := flag.String("m", defaultMsg, "message to display while keeping the machine awake")
	flag.Parse()
	if *total < time.Second {
		fmt.Fprintln(os.Stderr, "-t must be at least 1s")
		os.Exit(2)
	}

	cmd := inhibitCmd(*total, *titleMsg)
	if cmd == nil {
		fmt.Fprintln(os.Stderr, "unsupported platform:", runtime.GOOS)
		os.Exit(2)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// own process group so quitting also kills systemd-inhibit's `sleep` child
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "starting %s: %v\n", cmd.Args[0], err)
		os.Exit(1)
	}
	kill := func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	sp := spinner.New()
	sp.Spinner = spinner.Spinner{Frames: []string{"☕ ", "☕ ˚", "☕ ˚。", "☕ ˚。⋆", "☕  。⋆", "☕   ⋆"}, FPS: time.Second / 4}
	// fixed width so the text after the spinner doesn't shift as frames change length
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("180")).Width(7)

	m := model{titleMsg: *titleMsg, total: *total, start: time.Now(), spinner: sp,
		bar: progress.New(progress.WithGradient("#5A3E2B", "#F5A623"), progress.WithoutPercentage())}
	p := tea.NewProgram(m)
	go func() { p.Send(doneMsg{err: cmd.Wait()}) }()
	final, err := p.Run()
	kill()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := final.(model).err; err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n%s", cmd.Args[0], err, stderr.String())
		os.Exit(1)
	}
}
