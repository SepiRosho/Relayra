package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/store"
)

// SettingsView displays the current configuration with inline editing.
type SettingsView struct {
	cfg     *config.Config
	rdb     store.Backend
	cursor  int
	items   []settingItem
	editing bool
	editBuf string
	saved   bool
	saveErr error
}

type settingItem struct {
	Label string
	Value string
	Key   string // config key for editable items; empty = read-only
}

// editable config keys
var editableKeys = map[string]bool{
	"listen_addr":              true,
	"storage_backend":          true,
	"sqlite_path":              true,
	"redis_addr":               true,
	"redis_port":               true,
	"redis_db":                 true,
	"poll_interval":            true,
	"poll_batch_size":          true,
	"request_timeout":          true,
	"transport_mode":           true,
	"long_poll_wait":           true,
	"proxy_cooldown_seconds":   true,
	"async_workers":            true,
	"allow_listener_execution": true,
	"result_ttl":               true,
	"log_max_days":             true,
	"webhook_retries":          true,
}

type settingsSavedMsg struct {
	err error
}

// NewSettingsView creates a new settings viewer.
func NewSettingsView(cfg *config.Config, rdb store.Backend) *SettingsView {
	items := []settingItem{
		{"Role", string(cfg.Role), ""},
		{"Instance Name", cfg.InstanceName, ""},
		{"Machine ID", cfg.MachineID, ""},
		{"Listen Address", cfg.ListenAddress(), "listen_addr"},
		{"Storage Backend", cfg.StorageBackend, "storage_backend"},
		{"SQLite Path", cfg.SQLitePath, "sqlite_path"},
	}

	if cfg.PublicAddr != "" {
		items = append(items, settingItem{"Public Address", cfg.PublicAddress(), ""})
	}

	items = append(items, []settingItem{
		{"Redis Address", cfg.RedisAddr, "redis_addr"},
		{"Redis Port", fmt.Sprintf("%d", cfg.RedisPort), "redis_port"},
		{"Redis DB", fmt.Sprintf("%d", cfg.RedisDB), "redis_db"},
		{"Poll Interval", fmt.Sprintf("%d", cfg.PollInterval), "poll_interval"},
		{"Poll Batch Size", fmt.Sprintf("%d", cfg.PollBatchSize), "poll_batch_size"},
		{"Request Timeout", fmt.Sprintf("%d", cfg.RequestTimeout), "request_timeout"},
		{"Transport Mode", cfg.NormalizedTransportMode(), "transport_mode"},
		{"Long Poll Wait", fmt.Sprintf("%d", cfg.LongPollWait), "long_poll_wait"},
		{"Proxy Cooldown Seconds", fmt.Sprintf("%d", cfg.ProxyCooldownSeconds), "proxy_cooldown_seconds"},
		{"Async Workers", fmt.Sprintf("%d", cfg.AsyncWorkers), "async_workers"},
		{"Allow Listener Execution", fmt.Sprintf("%t", cfg.AllowListenerExecution), "allow_listener_execution"},
		{"Result TTL", fmt.Sprintf("%d", cfg.ResultTTL), "result_ttl"},
		{"Log Level", cfg.LogLevel, ""},
		{"Log Directory", cfg.LogDir, ""},
		{"Log Max Days", fmt.Sprintf("%d", cfg.LogMaxDays), "log_max_days"},
		{"Webhook Max Retries", fmt.Sprintf("%d", cfg.WebhookMaxRetries), "webhook_retries"},
		{"Config File", config.EnvPath(), ""},
	}...)

	return &SettingsView{
		cfg:   cfg,
		rdb:   rdb,
		items: items,
	}
}

func (sv *SettingsView) Init() tea.Cmd {
	return nil
}

func (sv *SettingsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case settingsSavedMsg:
		if msg.err != nil {
			sv.saveErr = msg.err
		} else {
			sv.saved = true
		}
		return sv, nil

	case tea.KeyMsg:
		if sv.editing {
			return sv.updateEditing(msg)
		}

		// Clear status on any key
		sv.saved = false
		sv.saveErr = nil

		switch msg.String() {
		case "up", "k":
			if sv.cursor > 0 {
				sv.cursor--
			}
		case "down", "j":
			if sv.cursor < len(sv.items)-1 {
				sv.cursor++
			}
		case "enter", "e":
			item := sv.items[sv.cursor]
			if item.Key != "" {
				sv.editing = true
				sv.editBuf = item.Value
			}
		}
	}
	return sv, nil
}

func (sv *SettingsView) updateEditing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		sv.editing = false
		return sv, nil
	case "enter":
		sv.editing = false
		return sv, sv.applyEdit()
	case "backspace":
		if len(sv.editBuf) > 0 {
			sv.editBuf = sv.editBuf[:len(sv.editBuf)-1]
		}
	default:
		if len(msg.String()) == 1 {
			sv.editBuf += msg.String()
		}
	}
	return sv, nil
}

func (sv *SettingsView) applyEdit() tea.Cmd {
	return func() tea.Msg {
		item := sv.items[sv.cursor]
		newVal := strings.TrimSpace(sv.editBuf)

		switch item.Key {
		case "listen_addr":
			parts := strings.Split(newVal, ":")
			sv.cfg.ListenAddr = parts[0]
			if len(parts) > 1 {
				if p, err := strconv.Atoi(parts[1]); err == nil {
					sv.cfg.ListenPort = p
				}
			}
			sv.items[sv.cursor].Value = sv.cfg.ListenAddress()
		case "redis_addr":
			sv.cfg.RedisAddr = newVal
			sv.items[sv.cursor].Value = newVal
		case "storage_backend":
			switch strings.ToLower(newVal) {
			case "redis", "sqlite":
				sv.cfg.StorageBackend = newVal
				sv.items[sv.cursor].Value = newVal
			}
		case "sqlite_path":
			if newVal != "" {
				sv.cfg.SQLitePath = newVal
				sv.items[sv.cursor].Value = newVal
			}
		case "redis_port":
			if v, err := strconv.Atoi(newVal); err == nil && v > 0 && v <= 65535 {
				sv.cfg.RedisPort = v
				sv.items[sv.cursor].Value = newVal
			}
		case "redis_db":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 0 {
				sv.cfg.RedisDB = v
				sv.items[sv.cursor].Value = newVal
			}
		case "poll_interval":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.PollInterval = v
				sv.items[sv.cursor].Value = newVal
			}
		case "poll_batch_size":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.PollBatchSize = v
				sv.items[sv.cursor].Value = newVal
			}
		case "request_timeout":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.RequestTimeout = v
				sv.items[sv.cursor].Value = newVal
			}
		case "transport_mode":
			mode := strings.ToLower(newVal)
			switch mode {
			case config.TransportModeInterval, config.TransportModeLongPoll, config.TransportModeWebSocket:
				sv.cfg.TransportMode = mode
				sv.cfg.LongPolling = mode == config.TransportModeLongPoll
				sv.items[sv.cursor].Value = mode
			}
		case "long_poll_wait":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.LongPollWait = v
				sv.items[sv.cursor].Value = newVal
			}
		case "proxy_cooldown_seconds":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.ProxyCooldownSeconds = v
				sv.items[sv.cursor].Value = newVal
			}
		case "async_workers":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.AsyncWorkers = v
				sv.items[sv.cursor].Value = newVal
			}
		case "allow_listener_execution":
			if v, ok := parseBool(newVal); ok {
				sv.cfg.AllowListenerExecution = v
				sv.items[sv.cursor].Value = fmt.Sprintf("%t", v)
			}
		case "result_ttl":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.ResultTTL = v
				sv.items[sv.cursor].Value = newVal
			}
		case "log_max_days":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 1 {
				sv.cfg.LogMaxDays = v
				sv.items[sv.cursor].Value = newVal
			}
		case "webhook_retries":
			if v, err := strconv.Atoi(newVal); err == nil && v >= 0 {
				sv.cfg.WebhookMaxRetries = v
				sv.items[sv.cursor].Value = newVal
			}
		}

		err := config.Save(sv.cfg)
		return settingsSavedMsg{err: err}
	}
}

func parseBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func (sv *SettingsView) View() string {
	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Settings"))
	b.WriteString("\n\n")

	for i, item := range sv.items {
		cursor := "  "
		style := normalStyle
		if i == sv.cursor {
			cursor = "▸ "
			style = selectedStyle
		}

		label := labelStyle.Render(item.Label + ":")

		if sv.editing && i == sv.cursor {
			b.WriteString(fmt.Sprintf("%s%s %s%s\n",
				cursor, label,
				selectedStyle.Render(sv.editBuf),
				dimStyle.Render("█"),
			))
			continue
		}

		editMarker := ""
		if item.Key != "" {
			editMarker = " ✎"
		}

		b.WriteString(fmt.Sprintf("%s%s %s%s\n",
			cursor, label,
			style.Render(item.Value),
			dimStyle.Render(editMarker),
		))
	}

	b.WriteString("\n")

	if sv.saved {
		b.WriteString(successStyle.Render("  Settings saved to " + config.EnvPath()))
		b.WriteString("\n")
		b.WriteString(warnStyle.Render("  Restart the service for changes to take effect."))
		b.WriteString("\n\n")
	}
	if sv.saveErr != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Save failed: %v", sv.saveErr)))
		b.WriteString("\n\n")
	}

	if sv.editing {
		b.WriteString(dimStyle.Render("  enter save • esc cancel"))
	} else {
		b.WriteString(dimStyle.Render("  enter/e edit • ↑/↓ scroll • esc back"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  ✎ = editable field"))
	}

	return b.String()
}
