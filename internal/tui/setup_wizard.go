package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/config"
)

// SetupStep identifies the current step in the setup wizard.
type SetupStep int

const (
	StepRole SetupStep = iota
	StepListenAddr
	StepListenPort
	StepPublicAddr
	StepInstanceName
	StepStorageBackend
	StepSQLitePath
	StepRedisAddr
	StepRedisPort
	StepRedisPassword
	StepLogLevel
	StepAllowListenerExecution
	StepConfirm
	StepDone
)

// SetupWizard is the Bubble Tea model for first-time configuration.
type SetupWizard struct {
	step             SetupStep
	cfg              *config.Config
	textInput        textinput.Model
	roleIdx          int
	storageIdx       int
	logIdx           int
	allowListenerIdx int
	err              error
	machineID        string
}

// NewSetupWizard creates a new setup wizard.
func NewSetupWizard() *SetupWizard {
	ti := textinput.New()
	ti.Focus()

	cfg := config.DefaultConfig()

	// Generate machine ID upfront
	machineID, err := config.GenerateMachineID()
	if err != nil {
		machineID = "error-generating-id"
	}
	cfg.MachineID = machineID

	return &SetupWizard{
		step:             StepRole,
		cfg:              cfg,
		textInput:        ti,
		storageIdx:       0,
		allowListenerIdx: 1,
		machineID:        machineID,
	}
}

func (w *SetupWizard) Init() tea.Cmd {
	return textinput.Blink
}

func (w *SetupWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return w, tea.Quit

		case "enter":
			return w.advance()

		case "up", "k":
			if w.step == StepRole && w.roleIdx > 0 {
				w.roleIdx--
			}
			if w.step == StepStorageBackend && w.storageIdx > 0 {
				w.storageIdx--
			}
			if w.step == StepLogLevel && w.logIdx > 0 {
				w.logIdx--
			}
			if w.step == StepAllowListenerExecution && w.allowListenerIdx > 0 {
				w.allowListenerIdx--
			}
			return w, nil

		case "down", "j":
			if w.step == StepRole && w.roleIdx < 1 {
				w.roleIdx++
			}
			if w.step == StepStorageBackend && w.storageIdx < 1 {
				w.storageIdx++
			}
			if w.step == StepLogLevel && w.logIdx < 3 {
				w.logIdx++
			}
			if w.step == StepAllowListenerExecution && w.allowListenerIdx < 1 {
				w.allowListenerIdx++
			}
			return w, nil
		}
	}

	// Pass to text input for text entry steps
	if w.isTextStep() {
		var cmd tea.Cmd
		w.textInput, cmd = w.textInput.Update(msg)
		return w, cmd
	}

	return w, nil
}

func (w *SetupWizard) isTextStep() bool {
	switch w.step {
	case StepListenAddr, StepListenPort, StepPublicAddr, StepInstanceName, StepSQLitePath, StepRedisAddr, StepRedisPort, StepRedisPassword:
		return true
	}
	return false
}

func (w *SetupWizard) advance() (tea.Model, tea.Cmd) {
	switch w.step {
	case StepRole:
		if w.roleIdx == 0 {
			w.cfg.Role = config.RoleListener
		} else {
			w.cfg.Role = config.RoleSender
		}
		w.step = StepListenAddr
		w.textInput.SetValue(w.cfg.ListenAddr)
		w.textInput.Focus()

	case StepListenAddr:
		v := strings.TrimSpace(w.textInput.Value())
		if v != "" {
			w.cfg.ListenAddr = v
		}
		w.step = StepListenPort
		w.textInput.SetValue(fmt.Sprintf("%d", w.cfg.ListenPort))

	case StepListenPort:
		v := strings.TrimSpace(w.textInput.Value())
		if v != "" {
			var port int
			fmt.Sscanf(v, "%d", &port)
			if port > 0 && port <= 65535 {
				w.cfg.ListenPort = port
			}
		}
		// Only ask for public address on Listener
		if w.cfg.Role == config.RoleListener {
			w.step = StepPublicAddr
			w.textInput.SetValue("")
			w.textInput.Placeholder = "e.g., 203.0.113.10 (required for Listener)"
		} else {
			w.step = StepInstanceName
			w.textInput.SetValue("")
			w.textInput.Placeholder = "e.g., HOME, OFFICE, PROD"
		}

	case StepPublicAddr:
		v := strings.TrimSpace(w.textInput.Value())
		if v == "" {
			w.err = fmt.Errorf("public address is required for Listener (the IP that Senders will connect to)")
			return w, nil
		}
		w.cfg.PublicAddr = v
		w.err = nil
		w.step = StepInstanceName
		w.textInput.SetValue("")
		w.textInput.Placeholder = "e.g., HOME, OFFICE, PROD"

	case StepInstanceName:
		v := strings.TrimSpace(w.textInput.Value())
		if v == "" {
			w.err = fmt.Errorf("instance name is required")
			return w, nil
		}
		if len(v) > 32 {
			w.err = fmt.Errorf("instance name must be 32 characters or fewer")
			return w, nil
		}
		w.cfg.InstanceName = v
		w.err = nil
		w.step = StepStorageBackend

	case StepStorageBackend:
		if w.storageIdx == 0 {
			w.cfg.StorageBackend = "redis"
			w.step = StepRedisAddr
			w.textInput.SetValue(w.cfg.RedisAddr)
			w.textInput.Placeholder = ""
		} else {
			w.cfg.StorageBackend = "sqlite"
			w.step = StepSQLitePath
			w.textInput.SetValue(w.cfg.SQLitePath)
			w.textInput.Placeholder = "e.g., /opt/relayra/relayra.db"
		}

	case StepSQLitePath:
		v := strings.TrimSpace(w.textInput.Value())
		if v == "" {
			w.err = fmt.Errorf("sqlite path is required when using SQLite")
			return w, nil
		}
		w.cfg.SQLitePath = v
		w.err = nil
		w.step = StepLogLevel

	case StepRedisAddr:
		v := strings.TrimSpace(w.textInput.Value())
		if v != "" {
			w.cfg.RedisAddr = v
		}
		w.step = StepRedisPort
		w.textInput.SetValue(fmt.Sprintf("%d", w.cfg.RedisPort))

	case StepRedisPort:
		v := strings.TrimSpace(w.textInput.Value())
		if v != "" {
			var port int
			fmt.Sscanf(v, "%d", &port)
			if port > 0 && port <= 65535 {
				w.cfg.RedisPort = port
			}
		}
		w.step = StepRedisPassword
		w.textInput.SetValue("")
		w.textInput.Placeholder = "(empty for no password)"
		w.textInput.EchoMode = textinput.EchoPassword

	case StepRedisPassword:
		w.cfg.RedisPassword = w.textInput.Value()
		w.textInput.EchoMode = textinput.EchoNormal
		w.step = StepLogLevel

	case StepLogLevel:
		levels := []string{"debug", "info", "warn", "error"}
		w.cfg.LogLevel = levels[w.logIdx]
		if w.cfg.Role == config.RoleListener {
			if w.cfg.AllowListenerExecution {
				w.allowListenerIdx = 0
			} else {
				w.allowListenerIdx = 1
			}
			w.step = StepAllowListenerExecution
		} else {
			w.step = StepConfirm
		}

	case StepAllowListenerExecution:
		w.cfg.AllowListenerExecution = w.allowListenerIdx == 0
		w.step = StepConfirm

	case StepConfirm:
		// Save configuration
		if err := config.Save(w.cfg); err != nil {
			w.err = fmt.Errorf("save config: %v", err)
			return w, nil
		}
		w.step = StepDone
		return w, tea.Quit
	}

	return w, nil
}

func (w *SetupWizard) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("  RELAYRA SETUP"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  Machine ID: %s", w.machineID[:16]+"...")))
	b.WriteString("\n\n")

	switch w.step {
	case StepRole:
		b.WriteString(subtitleStyle.Render("  Select server role:"))
		b.WriteString("\n\n")
		roles := []string{"Listener (unrestricted server)", "Sender (restricted server)"}
		for i, r := range roles {
			cursor := "  "
			style := normalStyle
			if i == w.roleIdx {
				cursor = "▸ "
				style = selectedStyle
			}
			b.WriteString(style.Render(cursor + r))
			b.WriteString("\n")
		}

	case StepListenAddr:
		b.WriteString(subtitleStyle.Render("  Listen address:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  IP address to bind (default: 0.0.0.0)"))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepListenPort:
		b.WriteString(subtitleStyle.Render("  Listen port:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Port number (random if empty)"))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepPublicAddr:
		b.WriteString(subtitleStyle.Render("  Public IP address:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  The external IP that Senders will use to reach this Listener."))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  This is embedded in pairing tokens. Do NOT use 0.0.0.0."))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepInstanceName:
		b.WriteString(subtitleStyle.Render("  Instance name (up to 32 chars):"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  A human-readable name for this server"))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepStorageBackend:
		b.WriteString(subtitleStyle.Render("  Storage backend:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Choose where Relayra stores peers, queues, results, and tokens"))
		b.WriteString("\n\n")
		backends := []string{"Redis", "SQLite"}
		descs := []string{
			"Networked store, good for multi-process/shared deployments",
			"Single-file local database, simpler standalone setup",
		}
		for i, backend := range backends {
			cursor := "  "
			style := normalStyle
			if i == w.storageIdx {
				cursor = "▸ "
				style = selectedStyle
			}
			b.WriteString(style.Render(fmt.Sprintf("%s%-8s", cursor, backend)))
			b.WriteString(dimStyle.Render(" " + descs[i]))
			b.WriteString("\n")
		}

	case StepSQLitePath:
		b.WriteString(subtitleStyle.Render("  SQLite database path:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  File path for the local Relayra database"))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepRedisAddr:
		b.WriteString(subtitleStyle.Render("  Redis address:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Redis server hostname or IP"))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepRedisPort:
		b.WriteString(subtitleStyle.Render("  Redis port:"))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepRedisPassword:
		b.WriteString(subtitleStyle.Render("  Redis password:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Leave empty for no password"))
		b.WriteString("\n\n  ")
		b.WriteString(w.textInput.View())

	case StepLogLevel:
		b.WriteString(subtitleStyle.Render("  Log level:"))
		b.WriteString("\n\n")
		levels := []string{"debug", "info", "warn", "error"}
		descs := []string{
			"Everything (verbose, for debugging)",
			"Normal operations (recommended)",
			"Warnings and errors only",
			"Errors only",
		}
		for i, level := range levels {
			cursor := "  "
			style := normalStyle
			if i == w.logIdx {
				cursor = "▸ "
				style = selectedStyle
			}
			b.WriteString(style.Render(fmt.Sprintf("%s%-8s", cursor, level)))
			b.WriteString(dimStyle.Render(" " + descs[i]))
			b.WriteString("\n")
		}

	case StepAllowListenerExecution:
		b.WriteString(subtitleStyle.Render("  Listener-side execution:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Allow this Listener to execute relay requests targeted at itself"))
		b.WriteString("\n\n")
		options := []string{"Allow", "Refuse"}
		descs := []string{
			"Enable duplex execution so requests can target this Listener",
			"Reject listener-targeted execution requests",
		}
		for i, option := range options {
			cursor := "  "
			style := normalStyle
			if i == w.allowListenerIdx {
				cursor = "▸ "
				style = selectedStyle
			}
			b.WriteString(style.Render(fmt.Sprintf("%s%-8s", cursor, option)))
			b.WriteString(dimStyle.Render(" " + descs[i]))
			b.WriteString("\n")
		}

	case StepConfirm:
		b.WriteString(subtitleStyle.Render("  Configuration Summary:"))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Machine ID:"), valueStyle.Render(w.machineID[:16]+"...")))
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Role:"), valueStyle.Render(string(w.cfg.Role))))
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Instance:"), valueStyle.Render(w.cfg.InstanceName)))
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Listen:"), valueStyle.Render(w.cfg.ListenAddress())))
		if w.cfg.PublicAddr != "" {
			b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Public:"), valueStyle.Render(w.cfg.PublicAddress())))
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Storage:"), valueStyle.Render(w.cfg.StorageBackend)))
		if w.cfg.StorageBackend == "sqlite" {
			b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("SQLite:"), valueStyle.Render(w.cfg.SQLitePath)))
		} else {
			b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Redis:"), valueStyle.Render(w.cfg.RedisURL())))
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Log Level:"), valueStyle.Render(w.cfg.LogLevel)))
		if w.cfg.Role == config.RoleListener {
			b.WriteString(fmt.Sprintf("  %s %t\n", labelStyle.Render("Listener Exec:"), w.cfg.AllowListenerExecution))
		}
		b.WriteString("\n")
		b.WriteString(infoStyle.Render("  Press Enter to save configuration"))

	case StepDone:
		b.WriteString(successStyle.Render("  Configuration saved!"))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("  Machine ID: %s\n", w.machineID))
		b.WriteString(fmt.Sprintf("  Config file: %s\n", config.EnvPath()))
		b.WriteString("\n")
		if w.cfg.Role == config.RoleListener {
			b.WriteString(dimStyle.Render("  Next: run 'relayra run' to start the Listener"))
		} else {
			b.WriteString(dimStyle.Render("  Next: add proxies with 'relayra proxy add <url>'"))
			b.WriteString("\n")
			b.WriteString(dimStyle.Render("  Then: pair with 'relayra pair connect <token>'"))
		}
	}

	if w.err != nil {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", w.err)))
	}

	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("  enter confirm • ↑/↓ navigate • ctrl+c cancel"))
	b.WriteString("\n")

	return b.String()
}

// RunSetupWizard starts the setup wizard TUI.
func RunSetupWizard() error {
	wizard := NewSetupWizard()
	p := tea.NewProgram(wizard, tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		return err
	}

	// Check if setup was completed
	if w, ok := m.(*SetupWizard); ok && w.step == StepDone {
		return nil
	}
	return fmt.Errorf("setup cancelled")
}
