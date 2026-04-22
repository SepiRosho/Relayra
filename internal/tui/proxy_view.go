package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
)

// ProxyView is the Bubble Tea model for managing proxies.
type ProxyView struct {
	manager    *proxy.Manager
	rdb        *store.Redis
	proxies    []proxyRow
	cursor     int
	err        error
	ready      bool
	showDetail bool
	detail     *ProxyDetailView
}

type proxyRow struct {
	URL      string
	Priority float64
	Fails    int
	Healthy  bool
}

type proxiesLoadedMsg struct {
	proxies []proxyRow
	err     error
}

// NewProxyView creates a new proxy management view.
func NewProxyView(rdb *store.Redis) *ProxyView {
	mgr := proxy.NewManager(rdb)
	return &ProxyView{manager: mgr, rdb: rdb}
}

func (pv *ProxyView) Init() tea.Cmd {
	return pv.loadProxies
}

func (pv *ProxyView) loadProxies() tea.Msg {
	list, err := pv.manager.List(context.Background())
	if err != nil {
		return proxiesLoadedMsg{err: err}
	}

	rows := make([]proxyRow, 0, len(list))
	for _, p := range list {
		rows = append(rows, proxyRow{
			URL:      p.URL,
			Priority: p.Priority,
			Fails:    p.FailCount,
			Healthy:  p.Healthy,
		})
	}
	return proxiesLoadedMsg{proxies: rows}
}

func (pv *ProxyView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Delegate to detail view if showing
	if pv.showDetail && pv.detail != nil {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if msg.String() == "esc" && !pv.detail.editing && !pv.detail.confirm {
				if pv.detail.deleted {
					// Return to list and refresh
					pv.showDetail = false
					pv.detail = nil
					pv.ready = false
					return pv, pv.loadProxies
				}
				// Check if user selected "Back"
				pv.showDetail = false
				pv.detail = nil
				pv.ready = false
				return pv, pv.loadProxies
			}
		}

		// Check if "Back" action was selected
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "enter" {
			if !pv.detail.editing && !pv.detail.confirm &&
				pv.detail.actionIdx < len(pv.detail.actions) &&
				pv.detail.actions[pv.detail.actionIdx] == "Back" {
				pv.showDetail = false
				pv.detail = nil
				pv.ready = false
				return pv, pv.loadProxies
			}
		}

		var m tea.Model
		var cmd tea.Cmd
		m, cmd = pv.detail.Update(msg)
		pv.detail = m.(*ProxyDetailView)
		return pv, cmd
	}

	switch msg := msg.(type) {
	case proxiesLoadedMsg:
		pv.proxies = msg.proxies
		pv.err = msg.err
		pv.ready = true
		return pv, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if pv.cursor > 0 {
				pv.cursor--
			}
		case "down", "j":
			if pv.cursor < len(pv.proxies)-1 {
				pv.cursor++
			}
		case "enter":
			if len(pv.proxies) > 0 && pv.cursor < len(pv.proxies) {
				pv.showDetail = true
				pv.detail = NewProxyDetailView(pv.rdb, pv.proxies[pv.cursor].URL)
				return pv, pv.detail.Init()
			}
		case "r":
			pv.ready = false
			return pv, pv.loadProxies
		}
	}
	return pv, nil
}

func (pv *ProxyView) View() string {
	if pv.showDetail && pv.detail != nil {
		return pv.detail.View()
	}

	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Proxies"))
	b.WriteString("\n\n")

	if !pv.ready {
		b.WriteString(dimStyle.Render("  Loading..."))
		return b.String()
	}

	if pv.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", pv.err)))
		return b.String()
	}

	if len(pv.proxies) == 0 {
		b.WriteString(dimStyle.Render("  No proxies configured."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  Use 'relayra proxy add <url>' to add a proxy."))
		return b.String()
	}

	// Header
	header := fmt.Sprintf("  %-40s  %-10s  %-6s  %-8s", "URL", "Priority", "Fails", "Status")
	b.WriteString(dimStyle.Render(header))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  " + strings.Repeat("─", 70)))
	b.WriteString("\n")

	for i, p := range pv.proxies {
		cursor := "  "
		style := normalStyle
		if i == pv.cursor {
			cursor = "▸ "
			style = selectedStyle
		}

		statusStr := "healthy"
		if !p.Healthy {
			statusStr = "failed"
		}

		line := fmt.Sprintf("%s%-40s  %-10.0f  %-6d  %-8s",
			cursor, truncate(p.URL, 40), p.Priority, p.Fails, statusStr)
		b.WriteString(style.Render(line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  r refresh • enter details • ↑/↓ navigate • esc back"))

	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
