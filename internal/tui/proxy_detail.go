package tui

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
)

// ProxyDetailView shows details of a single proxy with management options.
type ProxyDetailView struct {
	manager   *proxy.Manager
	rdb       *store.Redis
	proxyURL  string
	info      proxy.ProxyInfo
	err       error
	ready     bool
	actionIdx int
	actions   []string
	confirm   bool
	deleted   bool

	// Editing state
	editing    bool
	editField  int // 0=scheme+host, 1=username, 2=password
	editFields []editableField
	testResult string
	testing    bool
}

type editableField struct {
	Label string
	Value string
}

type proxyDetailLoadedMsg struct {
	info proxy.ProxyInfo
	err  error
}

type proxyDeletedMsg struct {
	err error
}

type proxyCooldownResetMsg struct {
	err error
}

type proxyTestResultMsg struct {
	err error
}

type proxyUpdatedMsg struct {
	newURL string
	err    error
}

// NewProxyDetailView creates a detail view for a specific proxy.
func NewProxyDetailView(rdb *store.Redis, proxyURL string) *ProxyDetailView {
	mgr := proxy.NewManager(rdb)

	pd := &ProxyDetailView{
		manager:  mgr,
		rdb:      rdb,
		proxyURL: proxyURL,
		actions: []string{
			"Edit URL",
			"Test Proxy",
			"Reset Cooldown",
			"Delete Proxy",
			"Back",
		},
	}
	pd.parseURLFields()
	return pd
}

func (pd *ProxyDetailView) parseURLFields() {
	parsed, err := url.Parse(pd.proxyURL)
	if err != nil {
		pd.editFields = []editableField{
			{"URL", pd.proxyURL},
			{"Username", ""},
			{"Password", ""},
		}
		return
	}

	hostURL := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}

	pd.editFields = []editableField{
		{"URL", hostURL},
		{"Username", username},
		{"Password", password},
	}
}

func (pd *ProxyDetailView) buildURL() string {
	if len(pd.editFields) < 3 {
		return pd.proxyURL
	}

	baseURL := pd.editFields[0].Value
	username := pd.editFields[1].Value
	password := pd.editFields[2].Value

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}

	if username != "" {
		if password != "" {
			parsed.User = url.UserPassword(username, password)
		} else {
			parsed.User = url.User(username)
		}
	} else {
		parsed.User = nil
	}

	return parsed.String()
}

func (pd *ProxyDetailView) Init() tea.Cmd {
	return pd.loadProxy
}

func (pd *ProxyDetailView) loadProxy() tea.Msg {
	list, err := pd.manager.List(context.Background())
	if err != nil {
		return proxyDetailLoadedMsg{err: err}
	}

	for _, p := range list {
		if p.URL == pd.proxyURL {
			return proxyDetailLoadedMsg{info: p}
		}
	}

	return proxyDetailLoadedMsg{err: fmt.Errorf("proxy not found")}
}

func (pd *ProxyDetailView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case proxyDetailLoadedMsg:
		pd.info = msg.info
		pd.err = msg.err
		pd.ready = true
		return pd, nil

	case proxyDeletedMsg:
		if msg.err != nil {
			pd.err = msg.err
		} else {
			pd.deleted = true
		}
		return pd, nil

	case proxyCooldownResetMsg:
		if msg.err != nil {
			pd.err = msg.err
		}
		return pd, pd.loadProxy

	case proxyTestResultMsg:
		pd.testing = false
		if msg.err != nil {
			pd.testResult = fmt.Sprintf("FAILED: %v", msg.err)
		} else {
			pd.testResult = "OK — proxy is working"
		}
		return pd, pd.loadProxy

	case proxyUpdatedMsg:
		if msg.err != nil {
			pd.err = msg.err
		} else {
			pd.proxyURL = msg.newURL
			pd.parseURLFields()
		}
		pd.editing = false
		return pd, pd.loadProxy

	case tea.KeyMsg:
		if pd.editing {
			return pd.updateEditing(msg)
		}

		if pd.confirm {
			switch msg.String() {
			case "y", "Y":
				pd.confirm = false
				return pd, func() tea.Msg {
					err := pd.manager.Remove(context.Background(), pd.proxyURL)
					return proxyDeletedMsg{err: err}
				}
			default:
				pd.confirm = false
				return pd, nil
			}
		}

		switch msg.String() {
		case "up", "k":
			if pd.actionIdx > 0 {
				pd.actionIdx--
			}
		case "down", "j":
			if pd.actionIdx < len(pd.actions)-1 {
				pd.actionIdx++
			}
		case "enter":
			return pd.executeAction()
		}
	}
	return pd, nil
}

func (pd *ProxyDetailView) updateEditing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		pd.editField = (pd.editField + 1) % len(pd.editFields)
	case "shift+tab":
		pd.editField = (pd.editField - 1 + len(pd.editFields)) % len(pd.editFields)
	case "esc":
		pd.editing = false
		pd.parseURLFields() // revert
		return pd, nil
	case "enter":
		// Save
		newURL := pd.buildURL()
		if newURL == pd.proxyURL {
			pd.editing = false
			return pd, nil
		}
		return pd, func() tea.Msg {
			err := pd.manager.UpdateURL(context.Background(), pd.proxyURL, newURL)
			return proxyUpdatedMsg{newURL: newURL, err: err}
		}
	case "backspace":
		field := &pd.editFields[pd.editField]
		if len(field.Value) > 0 {
			field.Value = field.Value[:len(field.Value)-1]
		}
	default:
		if len(msg.String()) == 1 {
			pd.editFields[pd.editField].Value += msg.String()
		}
	}
	return pd, nil
}

func (pd *ProxyDetailView) executeAction() (tea.Model, tea.Cmd) {
	switch pd.actions[pd.actionIdx] {
	case "Edit URL":
		pd.editing = true
		pd.editField = 0
		pd.testResult = ""
	case "Test Proxy":
		pd.testing = true
		pd.testResult = ""
		return pd, func() tea.Msg {
			err := pd.manager.Test(context.Background(), pd.proxyURL)
			return proxyTestResultMsg{err: err}
		}
	case "Reset Cooldown":
		return pd, func() tea.Msg {
			err := pd.manager.ResetCooldown(context.Background(), pd.proxyURL)
			return proxyCooldownResetMsg{err: err}
		}
	case "Delete Proxy":
		pd.confirm = true
	case "Back":
		// handled by parent
	}
	return pd, nil
}

func (pd *ProxyDetailView) View() string {
	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Proxy Detail"))
	b.WriteString("\n\n")

	if !pd.ready {
		b.WriteString(dimStyle.Render("  Loading..."))
		return b.String()
	}

	if pd.deleted {
		b.WriteString(successStyle.Render("  Proxy deleted successfully."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  esc back to proxies"))
		return b.String()
	}

	if pd.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", pd.err)))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  esc back"))
		return b.String()
	}

	if pd.editing {
		return pd.viewEditing()
	}

	// Proxy info
	parsed, _ := url.Parse(pd.proxyURL)
	displayHost := pd.proxyURL
	username := ""
	password := ""
	if parsed != nil && parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
		displayHost = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	}

	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("URL:"), valueStyle.Render(displayHost)))
	if username != "" {
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Username:"), valueStyle.Render(username)))
		masked := strings.Repeat("•", len(password))
		if password == "" {
			masked = "(none)"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Password:"), valueStyle.Render(masked)))
	}
	b.WriteString(fmt.Sprintf("  %s %.0f\n", labelStyle.Render("Priority:"), pd.info.Priority))

	statusStr := "healthy"
	statusStyle := successStyle
	if !pd.info.Healthy {
		statusStr = fmt.Sprintf("failed (fails: %d/%d)", pd.info.FailCount, 3)
		statusStyle = errorStyle
	}
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Status:"), statusStyle.Render(statusStr)))

	if !pd.info.LastChecked.IsZero() {
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Last Checked:"), valueStyle.Render(pd.info.LastChecked.Format("2006-01-02 15:04:05"))))
	}

	if pd.testResult != "" {
		b.WriteString("\n")
		style := successStyle
		if strings.HasPrefix(pd.testResult, "FAILED") {
			style = errorStyle
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Test Result:"), style.Render(pd.testResult)))
	}

	if pd.testing {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Testing proxy..."))
		b.WriteString("\n")
	}

	b.WriteString("\n")

	if pd.confirm {
		b.WriteString(warnStyle.Render("  Delete this proxy? (y/N)"))
		b.WriteString("\n")
		return b.String()
	}

	// Actions
	for i, action := range pd.actions {
		cursor := "  "
		style := normalStyle
		if i == pd.actionIdx {
			cursor = "▸ "
			style = selectedStyle
		}
		b.WriteString(style.Render(cursor + action))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  ↑/↓ navigate • enter select • esc back"))

	return b.String()
}

func (pd *ProxyDetailView) viewEditing() string {
	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Edit Proxy"))
	b.WriteString("\n\n")

	for i, field := range pd.editFields {
		cursor := "  "
		style := dimStyle
		if i == pd.editField {
			cursor = "▸ "
			style = selectedStyle
		}

		displayValue := field.Value
		if field.Label == "Password" && len(displayValue) > 0 && i != pd.editField {
			displayValue = strings.Repeat("•", len(displayValue))
		}

		b.WriteString(fmt.Sprintf("%s%s %s\n",
			cursor,
			labelStyle.Render(field.Label+":"),
			style.Render(displayValue+"█"),
		))
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  tab next field • enter save • esc cancel"))

	return b.String()
}
