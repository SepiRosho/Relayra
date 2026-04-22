package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/store"
)

// Dashboard is the Bubble Tea model for the live status view.
type Dashboard struct {
	cfg   *config.Config
	rdb   store.Backend
	info  dashboardInfo
	err   error
	ready bool
}

type dashboardInfo struct {
	Role           string
	InstanceName   string
	MachineID      string
	ListenAddr     string
	StorageBackend string
	StorageStatus  string
	PeerCount      int
	QueueSize      int
	ListenerName   string
	ListenerAddr   string
	ListenerPeerID string
	ListenerCaps   []string
}

type dashboardTickMsg struct{}
type dashboardInfoMsg struct {
	info dashboardInfo
	err  error
}

// NewDashboard creates a new dashboard view.
func NewDashboard(cfg *config.Config, rdb store.Backend) *Dashboard {
	return &Dashboard{
		cfg: cfg,
		rdb: rdb,
	}
}

func (d *Dashboard) Init() tea.Cmd {
	return d.fetchInfo
}

func (d *Dashboard) fetchInfo() tea.Msg {
	info := dashboardInfo{
		Role:           string(d.cfg.Role),
		InstanceName:   d.cfg.InstanceName,
		MachineID:      d.cfg.MachineID,
		ListenAddr:     d.cfg.ListenAddress(),
		StorageBackend: d.cfg.StorageBackend,
	}

	ctx := context.Background()

	// Check Redis
	if err := d.rdb.Health(ctx); err != nil {
		info.StorageStatus = "disconnected"
	} else {
		info.StorageStatus = "connected"
	}

	// Peer count
	peerList, err := d.rdb.ListPeers(ctx)
	if err == nil {
		info.PeerCount = len(peerList)
	}

	// Sender: show connected listener info
	if d.cfg.Role == config.RoleSender {
		listener, err := d.rdb.GetListenerInfo(ctx)
		if err == nil && listener != nil {
			info.ListenerName = listener.Name
			info.ListenerAddr = listener.Address
			info.ListenerPeerID = listener.ID
			info.ListenerCaps = listener.Capabilities
		}
	}

	return dashboardInfoMsg{info: info}
}

func (d *Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dashboardInfoMsg:
		d.info = msg.info
		d.err = msg.err
		d.ready = true
		return d, nil
	case tea.KeyMsg:
		if msg.String() == "r" {
			return d, d.fetchInfo
		}
	}
	return d, nil
}

func (d *Dashboard) View() string {
	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Dashboard"))
	b.WriteString("\n\n")

	if !d.ready {
		b.WriteString(dimStyle.Render("  Loading..."))
		return b.String()
	}

	if d.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", d.err)))
		return b.String()
	}

	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Role:"), valueStyle.Render(d.info.Role)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Instance:"), valueStyle.Render(d.info.InstanceName)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Machine ID:"), valueStyle.Render(d.info.MachineID)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Listen:"), valueStyle.Render(d.info.ListenAddr)))

	storageStyle := successStyle
	if d.info.StorageStatus != "connected" {
		storageStyle = errorStyle
	}
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Storage:"), valueStyle.Render(d.info.StorageBackend)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Backend:"), storageStyle.Render(d.info.StorageStatus)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Peers:"), valueStyle.Render(fmt.Sprintf("%d", d.info.PeerCount))))

	// Sender: show connected listener
	if d.info.ListenerName != "" {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  ── Connected Listener ──"))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Listener:"), valueStyle.Render(d.info.ListenerName)))
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Address:"), valueStyle.Render(d.info.ListenerAddr)))
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Peer ID:"), valueStyle.Render(d.info.ListenerPeerID)))
		if len(d.info.ListenerCaps) > 0 {
			b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Capabilities:"), valueStyle.Render(strings.Join(d.info.ListenerCaps, ", "))))
		}
	} else if d.cfg.Role == config.RoleSender {
		b.WriteString("\n")
		b.WriteString(warnStyle.Render("  No listener connected. Run 'relayra pair connect <token>' to pair."))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  r refresh • esc back"))

	return b.String()
}
