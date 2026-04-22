package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/config"
)

const maxLogLines = 100

// LogsView is the Bubble Tea model for viewing log files.
type LogsView struct {
	cfg      *config.Config
	lines    []string
	offset   int
	height   int
	err      error
	ready    bool
	logFile  string
	fileList []string
	fileIdx  int
	picking  bool
}

type logsLoadedMsg struct {
	lines   []string
	logFile string
	err     error
}

type logFilesMsg struct {
	files []string
}

// NewLogsView creates a new logs viewer.
func NewLogsView(cfg *config.Config) *LogsView {
	return &LogsView{
		cfg:    cfg,
		height: 20,
	}
}

func (lv *LogsView) Init() tea.Cmd {
	return lv.findLogFiles
}

func (lv *LogsView) findLogFiles() tea.Msg {
	logDir := lv.cfg.LogDir
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return logsLoadedMsg{err: fmt.Errorf("read log dir %s: %w", logDir, err)}
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			files = append(files, e.Name())
		}
	}

	// Sort descending (newest first)
	sort.Sort(sort.Reverse(sort.StringSlice(files)))

	if len(files) == 0 {
		return logsLoadedMsg{err: fmt.Errorf("no log files found in %s", logDir)}
	}

	return logFilesMsg{files: files}
}

func (lv *LogsView) loadLogFile(filename string) tea.Cmd {
	return func() tea.Msg {
		fullPath := filepath.Join(lv.cfg.LogDir, filename)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return logsLoadedMsg{err: fmt.Errorf("read %s: %w", filename, err)}
		}

		allLines := strings.Split(string(data), "\n")

		// Take last N lines
		start := 0
		if len(allLines) > maxLogLines {
			start = len(allLines) - maxLogLines
		}
		lines := allLines[start:]

		return logsLoadedMsg{lines: lines, logFile: filename}
	}
}

func (lv *LogsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case logFilesMsg:
		lv.fileList = msg.files
		if len(msg.files) > 0 {
			// Auto-load the newest log file
			return lv, lv.loadLogFile(msg.files[0])
		}
		lv.ready = true
		return lv, nil

	case logsLoadedMsg:
		lv.err = msg.err
		if msg.err == nil {
			lv.lines = msg.lines
			lv.logFile = msg.logFile
			lv.offset = max(0, len(lv.lines)-lv.height)
		}
		lv.ready = true
		lv.picking = false
		return lv, nil

	case tea.WindowSizeMsg:
		lv.height = msg.Height - 8
		if lv.height < 5 {
			lv.height = 5
		}
		return lv, nil

	case tea.KeyMsg:
		if lv.picking {
			return lv.updateFilePicker(msg)
		}
		switch msg.String() {
		case "up", "k":
			if lv.offset > 0 {
				lv.offset--
			}
		case "down", "j":
			if lv.offset < len(lv.lines)-lv.height {
				lv.offset++
			}
		case "g":
			lv.offset = 0
		case "G":
			lv.offset = max(0, len(lv.lines)-lv.height)
		case "f":
			if len(lv.fileList) > 1 {
				lv.picking = true
				lv.fileIdx = 0
			}
		case "r":
			if lv.logFile != "" {
				lv.ready = false
				return lv, lv.loadLogFile(lv.logFile)
			}
		}
	}
	return lv, nil
}

func (lv *LogsView) updateFilePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if lv.fileIdx > 0 {
			lv.fileIdx--
		}
	case "down", "j":
		if lv.fileIdx < len(lv.fileList)-1 {
			lv.fileIdx++
		}
	case "enter":
		lv.ready = false
		return lv, lv.loadLogFile(lv.fileList[lv.fileIdx])
	case "esc":
		lv.picking = false
	}
	return lv, nil
}

func (lv *LogsView) View() string {
	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Logs"))
	b.WriteString("\n\n")

	if !lv.ready {
		b.WriteString(dimStyle.Render("  Loading..."))
		return b.String()
	}

	if lv.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", lv.err)))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  esc back"))
		return b.String()
	}

	if lv.picking {
		b.WriteString(dimStyle.Render("  Select log file:"))
		b.WriteString("\n\n")
		for i, f := range lv.fileList {
			cursor := "  "
			style := normalStyle
			if i == lv.fileIdx {
				cursor = "▸ "
				style = selectedStyle
			}
			b.WriteString(style.Render(cursor + f))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  ↑/↓ navigate • enter select • esc cancel"))
		return b.String()
	}

	// Show current file name
	b.WriteString(dimStyle.Render(fmt.Sprintf("  File: %s  (last %d lines)", lv.logFile, len(lv.lines))))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  " + strings.Repeat("─", 70)))
	b.WriteString("\n")

	// Show visible lines
	end := lv.offset + lv.height
	if end > len(lv.lines) {
		end = len(lv.lines)
	}
	for _, line := range lv.lines[lv.offset:end] {
		styled := lv.styleLine(line)
		b.WriteString("  " + styled)
		b.WriteString("\n")
	}

	// Scroll indicator
	total := len(lv.lines)
	if total > lv.height {
		pct := 0
		if total-lv.height > 0 {
			pct = lv.offset * 100 / (total - lv.height)
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("\n  [%d%%] %d/%d lines", pct, lv.offset+lv.height, total)))
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  ↑/↓ scroll • g top • G bottom • r refresh • f switch file • esc back"))

	return b.String()
}

func (lv *LogsView) styleLine(line string) string {
	if strings.Contains(line, "\"level\":\"ERROR\"") || strings.Contains(line, "level=ERROR") {
		return errorStyle.Render(line)
	}
	if strings.Contains(line, "\"level\":\"WARN\"") || strings.Contains(line, "level=WARN") {
		return warnStyle.Render(line)
	}
	if strings.Contains(line, "\"level\":\"INFO\"") || strings.Contains(line, "level=INFO") {
		return normalStyle.Render(line)
	}
	return dimStyle.Render(line)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
