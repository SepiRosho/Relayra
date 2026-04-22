package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/store"
)

// PeersView is the Bubble Tea model for listing peers.
type PeersView struct {
	cfg        *config.Config
	rdb        store.Backend
	peers      []peerRow
	cursor     int
	err        error
	ready      bool
	detail     *PeerDetailView
	showDetail bool
}

type peerRow struct {
	ID           string
	Name         string
	MachineID    string
	LastSeen     string
	Capabilities []string
	IsListener   bool
}

type peersLoadedMsg struct {
	peers []peerRow
	err   error
}

// NewPeersView creates a new peers list view.
func NewPeersView(cfg *config.Config, rdb store.Backend) *PeersView {
	return &PeersView{cfg: cfg, rdb: rdb}
}

func (pv *PeersView) Init() tea.Cmd {
	return pv.loadPeers
}

func (pv *PeersView) loadPeers() tea.Msg {
	if pv.cfg.Role == config.RoleSender {
		listener, err := pv.rdb.GetListenerInfo(context.Background())
		if err != nil {
			return peersLoadedMsg{err: err}
		}
		if listener == nil {
			return peersLoadedMsg{}
		}

		return peersLoadedMsg{
			peers: []peerRow{{
				ID:           listener.ID,
				Name:         listener.Name,
				MachineID:    listener.MachineID,
				LastSeen:     "paired listener",
				Capabilities: listener.Capabilities,
				IsListener:   true,
			}},
		}
	}

	list, err := pv.rdb.ListPeers(context.Background())
	if err != nil {
		return peersLoadedMsg{err: err}
	}

	rows := make([]peerRow, 0, len(list))
	for _, p := range list {
		rows = append(rows, peerRow{
			ID:           p.ID,
			Name:         p.Name,
			MachineID:    p.MachineID,
			LastSeen:     p.LastSeen.Format("2006-01-02 15:04:05"),
			Capabilities: p.Capabilities,
		})
	}
	return peersLoadedMsg{peers: rows}
}

func (pv *PeersView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// If showing peer detail, delegate everything there
	if pv.showDetail && pv.detail != nil {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if msg.String() == "esc" {
				// Back to peer list
				pv.showDetail = false
				pv.detail = nil
				pv.ready = false
				return pv, pv.loadPeers
			}
		}
		var m tea.Model
		var cmd tea.Cmd
		m, cmd = pv.detail.Update(msg)
		pv.detail = m.(*PeerDetailView)
		return pv, cmd
	}

	switch msg := msg.(type) {
	case peersLoadedMsg:
		pv.peers = msg.peers
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
			if pv.cursor < len(pv.peers)-1 {
				pv.cursor++
			}
		case "r":
			pv.ready = false
			return pv, pv.loadPeers
		case "enter":
			if len(pv.peers) > 0 && pv.cursor < len(pv.peers) {
				peer := pv.peers[pv.cursor]
				pv.detail = NewPeerDetailView(pv.cfg, pv.rdb, peer.ID, peer.IsListener)
				pv.showDetail = true
				return pv, pv.detail.Init()
			}
		}
	}
	return pv, nil
}

func (pv *PeersView) View() string {
	// Show peer detail if active
	if pv.showDetail && pv.detail != nil {
		return pv.detail.View()
	}

	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Peers"))
	b.WriteString("\n\n")

	if !pv.ready {
		b.WriteString(dimStyle.Render("  Loading..."))
		return b.String()
	}

	if pv.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", pv.err)))
		return b.String()
	}

	if len(pv.peers) == 0 {
		if pv.cfg.Role == config.RoleSender {
			b.WriteString(dimStyle.Render("  No listener paired yet."))
			b.WriteString("\n\n")
			b.WriteString(dimStyle.Render("  Use 'relayra pair connect <token>' to connect to a listener."))
			return b.String()
		}

		b.WriteString(dimStyle.Render("  No peers connected yet."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  Use 'relayra pair generate' to create a pairing token."))
		return b.String()
	}

	for i, p := range pv.peers {
		cursor := "  "
		style := normalStyle
		if i == pv.cursor {
			cursor = "▸ "
			style = selectedStyle
		}
		line := fmt.Sprintf("%s%-20s  %s  %s", cursor, p.Name, p.MachineID[:16]+"...", p.LastSeen)
		b.WriteString(style.Render(line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  r refresh • ↑/↓ navigate • enter details • esc back"))

	return b.String()
}
