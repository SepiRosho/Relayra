package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/store"
)

// Screen identifies which TUI view is active.
type Screen int

const (
	ScreenMenu Screen = iota
	ScreenDashboard
	ScreenPeers
	ScreenProxies
	ScreenTokens
	ScreenLogs
	ScreenSettings
)

// App is the root Bubble Tea model for the main TUI panel.
type App struct {
	screen    Screen
	menuIdx   int
	menuItems []string
	role      string
	instance  string
	quitting  bool
	cfg       *config.Config
	rdb       store.Backend
	// Sub-views
	dashboard    *Dashboard
	peersView    *PeersView
	proxyView    *ProxyView
	tokensView   *TokensView
	logsView     *LogsView
	settingsView *SettingsView
}

// NewApp creates a new TUI app model.
func NewApp(cfg *config.Config, rdb store.Backend) *App {
	role := string(cfg.Role)
	instance := cfg.InstanceName
	items := []string{
		"Status Dashboard",
		"Manage Peers",
		"API Tokens",
		"View Logs",
		"Settings",
		"Exit",
	}
	// Insert proxy management for Sender role
	if role == "sender" {
		items = []string{
			"Status Dashboard",
			"Manage Peers",
			"Manage Proxies",
			"View Logs",
			"Settings",
			"Exit",
		}
	}

	return &App{
		screen:    ScreenMenu,
		menuItems: items,
		role:      role,
		instance:  instance,
		cfg:       cfg,
		rdb:       rdb,
	}
}

func (a *App) Init() tea.Cmd {
	return nil
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch a.screen {
		case ScreenMenu:
			return a.updateMenu(msg)
		default:
			// ESC returns to menu, but only if the sub-view doesn't consume it
			if msg.String() == "esc" {
				// PeersView may be showing a detail — check if it should handle ESC
				if a.screen == ScreenPeers && a.peersView != nil && a.peersView.showDetail {
					break // let sub-view handle it
				}
				// ProxyView may be showing a detail
				if a.screen == ScreenProxies && a.proxyView != nil && a.proxyView.showDetail {
					break // let sub-view handle it
				}
				// LogsView may be in file picker
				if a.screen == ScreenLogs && a.logsView != nil && a.logsView.picking {
					break // let sub-view handle it
				}
				// SettingsView may be editing
				if a.screen == ScreenSettings && a.settingsView != nil && a.settingsView.editing {
					break // let sub-view handle it
				}
				// TokensView may be creating
				if a.screen == ScreenTokens && a.tokensView != nil && a.tokensView.creating {
					break // let sub-view handle it
				}
				a.screen = ScreenMenu
				return a, nil
			}
		}
	}

	// Delegate to active sub-view
	var cmd tea.Cmd
	switch a.screen {
	case ScreenDashboard:
		if a.dashboard != nil {
			var m tea.Model
			m, cmd = a.dashboard.Update(msg)
			a.dashboard = m.(*Dashboard)
		}
	case ScreenPeers:
		if a.peersView != nil {
			var m tea.Model
			m, cmd = a.peersView.Update(msg)
			a.peersView = m.(*PeersView)
		}
	case ScreenProxies:
		if a.proxyView != nil {
			var m tea.Model
			m, cmd = a.proxyView.Update(msg)
			a.proxyView = m.(*ProxyView)
		}
	case ScreenTokens:
		if a.tokensView != nil {
			var m tea.Model
			m, cmd = a.tokensView.Update(msg)
			a.tokensView = m.(*TokensView)
		}
	case ScreenLogs:
		if a.logsView != nil {
			var m tea.Model
			m, cmd = a.logsView.Update(msg)
			a.logsView = m.(*LogsView)
		}
	case ScreenSettings:
		if a.settingsView != nil {
			var m tea.Model
			m, cmd = a.settingsView.Update(msg)
			a.settingsView = m.(*SettingsView)
		}
	}
	return a, cmd
}

func (a *App) updateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if a.menuIdx > 0 {
			a.menuIdx--
		}
	case "down", "j":
		if a.menuIdx < len(a.menuItems)-1 {
			a.menuIdx++
		}
	case "enter":
		return a.selectMenuItem()
	case "q", "ctrl+c":
		a.quitting = true
		return a, tea.Quit
	}
	return a, nil
}

func (a *App) selectMenuItem() (tea.Model, tea.Cmd) {
	selected := a.menuItems[a.menuIdx]
	switch selected {
	case "Status Dashboard":
		a.screen = ScreenDashboard
		a.dashboard = NewDashboard(a.cfg, a.rdb)
		return a, a.dashboard.Init()
	case "Manage Peers":
		a.screen = ScreenPeers
		a.peersView = NewPeersView(a.cfg, a.rdb)
		return a, a.peersView.Init()
	case "Manage Proxies":
		a.screen = ScreenProxies
		a.proxyView = NewProxyView(a.cfg, a.rdb)
		return a, a.proxyView.Init()
	case "API Tokens":
		a.screen = ScreenTokens
		a.tokensView = NewTokensView(a.rdb)
		return a, a.tokensView.Init()
	case "View Logs":
		a.screen = ScreenLogs
		a.logsView = NewLogsView(a.cfg)
		return a, a.logsView.Init()
	case "Settings":
		a.screen = ScreenSettings
		a.settingsView = NewSettingsView(a.cfg, a.rdb)
		return a, a.settingsView.Init()
	case "Exit":
		a.quitting = true
		return a, tea.Quit
	}
	return a, nil
}

func (a *App) View() string {
	if a.quitting {
		return dimStyle.Render("Goodbye.\n")
	}

	switch a.screen {
	case ScreenMenu:
		return a.viewMenu()
	case ScreenDashboard:
		if a.dashboard != nil {
			return a.dashboard.View()
		}
	case ScreenPeers:
		if a.peersView != nil {
			return a.peersView.View()
		}
	case ScreenProxies:
		if a.proxyView != nil {
			return a.proxyView.View()
		}
	case ScreenTokens:
		if a.tokensView != nil {
			return a.tokensView.View()
		}
	case ScreenLogs:
		if a.logsView != nil {
			return a.logsView.View()
		}
	case ScreenSettings:
		if a.settingsView != nil {
			return a.settingsView.View()
		}
	}
	return ""
}

func (a *App) viewMenu() string {
	var b strings.Builder

	// ASCII banner
	banner := `
  ██████╗ ███████╗██╗      █████╗ ██╗   ██╗██████╗  █████╗
  ██╔══██╗██╔════╝██║     ██╔══██╗╚██╗ ██╔╝██╔══██╗██╔══██╗
  ██████╔╝█████╗  ██║     ███████║ ╚████╔╝ ██████╔╝███████║
  ██╔══██╗██╔══╝  ██║     ██╔══██║  ╚██╔╝  ██╔══██╗██╔══██║
  ██║  ██║███████╗███████╗██║  ██║   ██║   ██║  ██║██║  ██║
  ╚═╝  ╚═╝╚══════╝╚══════╝╚═╝  ╚═╝   ╚═╝   ╚═╝  ╚═╝╚═╝  ╚═╝`

	b.WriteString(titleStyle.Render(banner))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render(fmt.Sprintf("  %s • %s", strings.ToUpper(a.role), a.instance)))
	b.WriteString("\n\n")

	for i, item := range a.menuItems {
		cursor := "  "
		style := normalStyle
		if i == a.menuIdx {
			cursor = "▸ "
			style = selectedStyle
		}
		b.WriteString(style.Render(cursor + item))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  ↑/↓ navigate • enter select • q quit"))
	b.WriteString("\n")

	return b.String()
}

// RunTUI starts the main TUI application.
func RunTUI(cfg *config.Config, rdb store.Backend) error {
	app := NewApp(cfg, rdb)
	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
