package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"simworkflow/internal/argo"
	"simworkflow/internal/config"
	"simworkflow/internal/storage"
)

func interactiveOutput(in *os.File, out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && term.IsTerminal(in.Fd()) && term.IsTerminal(f.Fd()) && os.Getenv("TERM") != "dumb"
}

type progressFrame struct {
	name, phase, columns string
	rows                 []string
	err                  error
}

type progressTick struct{}

type progressModel struct {
	fetch               tea.Cmd
	frame               progressFrame
	width, height, x, y int
	done                bool
	refreshErr          error
}

func loadProgress(ctx context.Context, c config.Config, name string) tea.Cmd {
	client, store := argo.New(c), storage.New(c)
	return func() tea.Msg {
		snapshot, err := client.Get(ctx, name)
		if err != nil {
			return progressFrame{err: err}
		}
		var body strings.Builder
		if err := writeRunProgress(ctx, store, snapshot, &body); err != nil {
			return progressFrame{err: err}
		}
		phase := snapshot.Status.Phase
		if phase == "" {
			phase = "Pending"
		}
		frame := progressFrame{name: screenText(snapshot.Metadata.Name), phase: screenText(phase)}
		lines := strings.Split(strings.TrimSuffix(screenText(body.String()), "\n"), "\n")
		if strings.HasPrefix(lines[0], "RUN ") {
			frame.columns, frame.rows = lines[0], lines[1:]
		} else {
			frame.rows = lines
		}
		return frame
	}
}

// Keep remote values from injecting terminal controls. Tabwriter has already
// expanded the table's tabs before this point.
func screenText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s)
}

func watchProgress(ctx context.Context, c config.Config, name string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := progressModel{fetch: loadProgress(ctx, c, name), frame: progressFrame{name: screenText(name), phase: "Loading"}, width: 80, height: 24}
	final, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err != nil {
		return err
	}
	result := final.(progressModel)
	if result.frame.phase == "Failed" || result.frame.phase == "Error" {
		return errors.New("workflow did not succeed; inspect experiment results and Argo logs")
	}
	return result.refreshErr
}

func (m progressModel) Init() tea.Cmd { return m.fetch }

func (m progressModel) bodyHeight() int { return max(0, m.height-4) }

func (m *progressModel) clamp() {
	m.y = min(max(0, m.y), max(0, len(m.frame.rows)-m.bodyHeight()))
	maxWidth := ansi.StringWidth(m.frame.columns)
	for _, row := range m.frame.rows {
		maxWidth = max(maxWidth, ansi.StringWidth(row))
	}
	m.x = min(max(0, m.x), max(0, maxWidth-m.width))
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case progressFrame:
		m.refreshErr = msg.err
		if msg.err == nil {
			m.frame = msg
			m.done = msg.phase == "Succeeded" || msg.phase == "Failed" || msg.phase == "Error"
		}
		if !m.done {
			next = tea.Tick(5*time.Second, func(time.Time) tea.Msg { return progressTick{} })
		}
	case progressTick:
		if !m.done {
			next = m.fetch
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			m.y--
		case "down", "j":
			m.y++
		case "pgup", "ctrl+b":
			m.y -= max(1, m.bodyHeight())
		case "pgdown", "ctrl+f", " ":
			m.y += max(1, m.bodyHeight())
		case "home", "g":
			m.y = 0
		case "end", "G":
			m.y = len(m.frame.rows)
		case "left", "h":
			m.x -= 8
		case "right", "l":
			m.x += 8
		}
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.y -= 3
			case tea.MouseButtonWheelDown:
				m.y += 3
			case tea.MouseButtonWheelLeft:
				m.x -= 8
			case tea.MouseButtonWheelRight:
				m.x += 8
			}
		}
	}
	m.clamp()
	return m, next
}

func (m progressModel) View() string {
	status := "Refreshing every 5s"
	if m.done {
		status = "Finished; press q to close"
	} else if m.refreshErr != nil {
		status = "Refresh failed; retrying in 5s (showing last data)"
	}
	lines := []string{fmt.Sprintf("Workflow: %s  %s", m.frame.name, m.frame.phase), status, ansi.Cut(m.frame.columns, m.x, m.x+m.width)}
	for i := 0; i < m.bodyHeight(); i++ {
		line := ""
		if m.y+i < len(m.frame.rows) {
			line = ansi.Cut(m.frame.rows[m.y+i], m.x, m.x+m.width)
		}
		lines = append(lines, line)
	}
	first, last := 0, min(len(m.frame.rows), m.y+m.bodyHeight())
	if last > 0 {
		first = m.y + 1
	}
	lines = append(lines, fmt.Sprintf("%d-%d/%d | ↑↓/wheel scroll  ←→ pan  PgUp/PgDn  Home/End  q quit", first, last, len(m.frame.rows)))
	lines = lines[:min(len(lines), m.height)]
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
}
