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
	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

func interactiveOutput(in *os.File, out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && term.IsTerminal(in.Fd()) && term.IsTerminal(f.Fd()) && os.Getenv("TERM") != "dumb"
}

type progressFrame struct {
	name, phase, columns  string
	startedAt, finishedAt time.Time
	rows                  []string
	progressRows          []runProgressRow
	err                   error
	retryProgress         bool
	plan                  *experiment.Plan
	runs                  []experiment.Run
	estimate              runEstimate
}

type progressTick struct{}
type progressClockTick time.Time

func progressClock() tea.Cmd {
	return tea.Tick(time.Second, func(now time.Time) tea.Msg { return progressClockTick(now) })
}

type progressModel struct {
	fetch               tea.Cmd
	frame               progressFrame
	width, height, x, y int
	done                bool
	refreshErr          error
	selected            int
	details             []string
	detailRunID         string
	listX, listY        int
	openViewer          func(string, experiment.Run) tea.Cmd
	viewerBusy          bool
	viewerNotice        string
	now                 time.Time
}

func loadProgress(ctx context.Context, c config.Config, name string) tea.Cmd {
	client, store := argo.New(c), storage.New(c)
	estimator := newRunEstimator()
	return func() tea.Msg {
		snapshot, err := client.Get(ctx, name)
		if err != nil {
			return progressFrame{err: err}
		}
		prefix := snapshot.Metadata.Annotations["simworkflow.io/result-prefix"]
		var plan *experiment.Plan
		var rows []runProgressRow
		retryProgress := false
		if strings.HasPrefix(prefix, "experiments/") && !strings.Contains(prefix, "..") {
			readCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			plan = new(experiment.Plan)
			err := store.GetJSON(readCtx, prefix+"/plan.json", plan)
			cancel()
			if err != nil {
				return progressFrame{err: err}
			}
			rows = readPlannedRunProgress(ctx, store, snapshot, plan)
			for _, row := range rows {
				retryProgress = retryProgress || row.retry
			}
		}
		phase := snapshot.Status.Phase
		if phase == "" {
			phase = "Pending"
		}
		frame := progressFrame{name: screenText(snapshot.Metadata.Name), phase: screenText(phase), plan: plan, retryProgress: retryProgress, startedAt: snapshot.Status.StartedAt, finishedAt: snapshot.Status.FinishedAt}
		frame.progressRows = rows
		frame.estimate = estimator.observe(rows, plan, phase, time.Now())
		if plan != nil {
			for _, run := range plan.Runs {
				if statusRunID.MatchString(run.ID) {
					frame.runs = append(frame.runs, run)
				}
			}
		}
		frame.updateRows(time.Now())
		return frame
	}
}

// Reformat cached rows when the local clock ticks; no extra Argo/S3 requests.
func (f *progressFrame) updateRows(now time.Time) {
	if f.progressRows == nil {
		return
	}
	var body strings.Builder
	writeProgressRows(&body, f.progressRows, now)
	lines := strings.Split(strings.TrimSuffix(screenText(body.String()), "\n"), "\n")
	f.columns, f.rows = lines[0], lines[1:]
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
	m := progressModel{fetch: loadProgress(ctx, c, name), openViewer: simScopeCommand(ctx, c), frame: progressFrame{name: screenText(name), phase: "Loading"}, width: 80, height: 24}
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

func (m progressModel) Init() tea.Cmd { return tea.Batch(m.fetch, progressClock()) }

func (m progressModel) overviewLines() []string {
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	return progressOverview(m.frame.progressRows, m.frame.estimate, m.frame.phase, m.frame.startedAt, now, m.width)
}

func (m progressModel) bodyHeight() int { return max(0, m.height-4-len(m.overviewLines())) }

func (m progressModel) bodyRows() []string {
	if m.details != nil {
		return m.details
	}
	return m.frame.rows
}

func (m *progressModel) clamp() {
	rows := m.bodyRows()
	m.y = min(max(0, m.y), max(0, len(rows)-m.bodyHeight()))
	m.selected = min(max(0, m.selected), max(0, len(m.frame.rows)-1))
	maxWidth := 0
	width := m.width
	if m.details == nil {
		maxWidth = ansi.StringWidth(m.frame.columns)
		width = max(1, width-2)
	}
	for _, row := range rows {
		maxWidth = max(maxWidth, ansi.StringWidth(row))
	}
	m.x = min(max(0, m.x), max(0, maxWidth-width))
}

func (m *progressModel) moveSelection(delta int) {
	if m.details != nil {
		m.y += delta
		return
	}
	m.selected = min(max(0, m.selected+delta), max(0, len(m.frame.rows)-1))
	if m.selected < m.y {
		m.y = m.selected
	} else if m.selected >= m.y+max(1, m.bodyHeight()) {
		m.y = m.selected - max(1, m.bodyHeight()) + 1
	}
}

func (m *progressModel) closeDetails() {
	m.details, m.detailRunID = nil, ""
	m.x, m.y = m.listX, m.listY
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	switch msg := msg.(type) {
	case progressClockTick:
		m.now = time.Time(msg)
		m.frame.updateRows(m.now)
		if !m.done {
			next = progressClock()
		}
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case progressFrame:
		m.refreshErr = msg.err
		if msg.err == nil {
			// Keep selection attached to a run if the incoming rows are reordered.
			if m.selected < len(m.frame.runs) {
				id := m.frame.runs[m.selected].ID
				for i, run := range msg.runs {
					if run.ID == id {
						if m.details == nil {
							m.y += i - m.selected
						} else {
							m.listY += i - m.selected
						}
						m.selected = i
						break
					}
				}
			}
			m.frame = msg
			m.done = msg.phase == "Succeeded" || msg.phase == "Failed" || msg.phase == "Error"
		}
		if !m.done || m.frame.retryProgress {
			next = tea.Tick(5*time.Second, func(time.Time) tea.Msg { return progressTick{} })
		}
	case progressTick:
		if !m.done || m.frame.retryProgress {
			next = m.fetch
		}
	case viewerOpened:
		m.viewerBusy = false
		m.viewerNotice = "Opened SimScope in browser"
		if msg.err != nil {
			m.viewerNotice = "SimScope: " + screenText(msg.err.Error())
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace":
			if m.details != nil {
				m.closeDetails()
			} else if msg.String() == "esc" {
				return m, tea.Quit
			}
		case "enter":
			if m.details == nil && m.selected < len(m.frame.runs) && m.frame.plan != nil {
				run := m.frame.runs[m.selected]
				m.details = runDetails(m.frame.plan, run)
				m.detailRunID = run.ID
				m.listX, m.listY, m.x, m.y = m.x, m.y, 0, 0
			}
		case "w":
			if m.details != nil && !m.viewerBusy && m.openViewer != nil {
				for _, run := range m.frame.runs {
					if run.ID == m.detailRunID {
						m.viewerBusy = true
						m.viewerNotice = "Preparing SimScope (first replay may take a few minutes)..."
						next = m.openViewer(m.frame.name, run)
						break
					}
				}
			}
		case "up", "k":
			m.moveSelection(-1)
		case "down", "j":
			m.moveSelection(1)
		case "pgup", "ctrl+b":
			m.y -= max(1, m.bodyHeight())
		case "pgdown", "ctrl+f", " ":
			m.y += max(1, m.bodyHeight())
		case "home", "g":
			m.y = 0
			if m.details == nil {
				m.selected = 0
			}
		case "end", "G":
			m.y = len(m.bodyRows())
			if m.details == nil {
				m.selected = max(0, len(m.frame.rows)-1)
			}
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
			case tea.MouseButtonLeft:
				firstRow := 3 + len(m.overviewLines())
				if m.details == nil && msg.Y >= firstRow && msg.Y < firstRow+m.bodyHeight() && m.y+msg.Y-firstRow < len(m.frame.rows) {
					m.selected = m.y + msg.Y - firstRow
				}
			}
		}
	}
	m.clamp()
	if m.details == nil && m.bodyHeight() > 0 {
		m.selected = max(0, min(max(m.selected, m.y), min(len(m.frame.rows)-1, m.y+m.bodyHeight()-1)))
	}
	return m, next
}

func (m progressModel) View() string {
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	elapsed := workflowElapsed(m.frame.startedAt, m.frame.finishedAt, now, m.frame.phase)
	status := "Refreshing every 5s"
	if m.done {
		status = "Finished; press q to close"
		if m.frame.retryProgress {
			status = "Finished; retrying unavailable results every 5s"
		}
	} else if m.refreshErr != nil {
		status = "Refresh failed; retrying in 5s (showing last data)"
	}
	if m.viewerNotice != "" {
		status = m.viewerNotice
	}
	columns := "  " + ansi.Cut(m.frame.columns, m.x, m.x+max(0, m.width-2))
	if m.details != nil {
		columns = "Settings: " + m.detailRunID
	}
	rows := m.bodyRows()
	lines := []string{fmt.Sprintf("Workflow: %s  %s", m.frame.name, m.frame.phase), "Workflow Elapsed: " + elapsed + " | " + status}
	lines = append(lines, m.overviewLines()...)
	lines = append(lines, columns)
	for i := 0; i < m.bodyHeight(); i++ {
		line := ""
		if m.y+i < len(rows) {
			if m.details != nil {
				line = ansi.Cut(rows[m.y+i], m.x, m.x+m.width)
			} else {
				marker := "  "
				if m.y+i == m.selected {
					marker = "> "
				}
				line = marker + ansi.Cut(rows[m.y+i], m.x, m.x+max(0, m.width-2))
			}
		}
		lines = append(lines, line)
	}
	first, last := 0, min(len(rows), m.y+m.bodyHeight())
	if last > 0 {
		first = m.y + 1
	}
	help := "↑↓ select  Enter details  ←→ pan  PgUp/PgDn  q quit"
	if m.details != nil {
		help = "w SimScope  ↑↓/wheel scroll  ←→ pan  Esc back  q quit"
	}
	lines = append(lines, fmt.Sprintf("%d-%d/%d | %s", first, last, len(rows), help))
	lines = lines[:min(len(lines), m.height)]
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
}
