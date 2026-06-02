package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
)

// PeerDetailView shows details of a single peer with management options.
type PeerDetailView struct {
	cfg              *config.Config
	rdb              store.Backend
	peer             *models.Peer
	peerID           string
	isListener       bool
	err              error
	ready            bool
	actionIdx        int
	actions          []string
	confirm          bool
	confirmAction    string
	deleted          bool
	queueSize        int64
	statusMsg        string
	speedTestRunning bool
	speedTestResult  *speedTestResult
}

type speedTestResult struct {
	downloadMBps float64
	uploadMBps   float64
	proxyURL     string
	testBytes    int
	err          error
}

type peerDetailMsg struct {
	peer      *models.Peer
	queueSize int64
	err       error
}

type speedTestMsg struct {
	result *speedTestResult
}

type peerDeletedMsg struct {
	err error
}

type peerQueueClearedMsg struct {
	cleared int64
	err     error
}

// NewPeerDetailView creates a detail view for a specific peer.
func NewPeerDetailView(cfg *config.Config, rdb store.Backend, peerID string, isListener bool) *PeerDetailView {
	actions := []string{"Refresh"}
	if cfg.Role == config.RoleListener && !isListener {
		actions = []string{"Refresh", "Clear Queue", "Delete Peer"}
	}
	if cfg.Role == config.RoleSender && isListener {
		actions = []string{"Refresh", "Speed Test"}
	}

	return &PeerDetailView{
		cfg:        cfg,
		rdb:        rdb,
		peerID:     peerID,
		isListener: isListener,
		actions:    actions,
	}
}

func (pd *PeerDetailView) Init() tea.Cmd {
	return pd.loadPeer
}

func (pd *PeerDetailView) loadPeer() tea.Msg {
	ctx := context.Background()
	var (
		peer *models.Peer
		err  error
	)
	if pd.isListener {
		peer, err = pd.rdb.GetListenerInfo(ctx)
	} else {
		peer, err = pd.rdb.GetPeer(ctx, pd.peerID)
	}
	if err != nil {
		return peerDetailMsg{err: err}
	}

	var queueSize int64
	if pd.isListener {
		queueSize, _ = pd.rdb.PendingResultsCount(ctx)
	} else {
		queueSize, _ = pd.rdb.QueueLength(ctx, pd.peerID)
	}

	return peerDetailMsg{peer: peer, queueSize: queueSize}
}

func (pd *PeerDetailView) deletePeer() tea.Msg {
	if pd.isListener {
		return peerDeletedMsg{}
	}
	err := pd.rdb.DeletePeer(context.Background(), pd.peerID)
	return peerDeletedMsg{err: err}
}

func (pd *PeerDetailView) clearQueue() tea.Msg {
	cleared, err := pd.rdb.ClearPeerQueue(context.Background(), pd.peerID)
	return peerQueueClearedMsg{cleared: cleared, err: err}
}

func (pd *PeerDetailView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case peerDetailMsg:
		pd.peer = msg.peer
		pd.queueSize = msg.queueSize
		pd.err = msg.err
		pd.ready = true
		return pd, nil

	case peerDeletedMsg:
		if msg.err != nil {
			pd.err = msg.err
		} else {
			pd.deleted = true
		}
		return pd, nil

	case speedTestMsg:
		pd.speedTestRunning = false
		pd.speedTestResult = msg.result
		return pd, nil

	case peerQueueClearedMsg:
		if msg.err != nil {
			pd.err = msg.err
		} else {
			pd.statusMsg = fmt.Sprintf("Cleared %d queued request(s).", msg.cleared)
			pd.ready = false
			return pd, pd.loadPeer
		}
		return pd, nil

	case tea.KeyMsg:
		if pd.confirm {
			switch msg.String() {
			case "y", "Y":
				action := pd.confirmAction
				pd.confirm = false
				pd.confirmAction = ""
				switch action {
				case "delete":
					return pd, pd.deletePeer
				case "clear":
					return pd, pd.clearQueue
				default:
					return pd, nil
				}
			default:
				pd.confirm = false
				pd.confirmAction = ""
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

func (pd *PeerDetailView) executeAction() (tea.Model, tea.Cmd) {
	switch pd.actions[pd.actionIdx] {
	case "Refresh":
		pd.ready = false
		pd.statusMsg = ""
		return pd, pd.loadPeer
	case "Clear Queue":
		pd.confirm = true
		pd.confirmAction = "clear"
	case "Delete Peer":
		pd.confirm = true
		pd.confirmAction = "delete"
	case "Speed Test":
		pd.speedTestRunning = true
		pd.speedTestResult = nil
		return pd, pd.runSpeedTest
	}
	return pd, nil
}

func (pd *PeerDetailView) runSpeedTest() tea.Msg {
	const testBytes = 5 * 1024 * 1024 // 5 MB

	ctx := context.Background()
	proxyMgr := proxy.NewManager(pd.rdb, pd.cfg.ProxyCooldown(), pd.cfg.AllowDirectConnection)
	tr, proxyURL, err := proxyMgr.GetTransport(ctx)
	if err != nil {
		return speedTestMsg{result: &speedTestResult{err: fmt.Errorf("no transport available: %w", err)}}
	}

	if pd.peer == nil {
		return speedTestMsg{result: &speedTestResult{err: fmt.Errorf("peer not loaded")}}
	}

	client := &http.Client{Transport: tr, Timeout: 120 * time.Second}
	base := "http://" + pd.peer.Address

	// Download test
	dlURL := fmt.Sprintf("%s/api/v1/speedtest/download?size=%d", base, testBytes)
	dlStart := time.Now()
	resp, err := client.Get(dlURL)
	if err != nil {
		return speedTestMsg{result: &speedTestResult{proxyURL: proxyURL, err: fmt.Errorf("download failed: %w", err)}}
	}
	dlBytes, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("download failed: HTTP %d", resp.StatusCode)
		if err != nil {
			msg = fmt.Sprintf("download failed: %v", err)
		}
		return speedTestMsg{result: &speedTestResult{proxyURL: proxyURL, err: fmt.Errorf("%s", msg)}}
	}
	dlDuration := time.Since(dlStart)
	downloadMBps := float64(dlBytes) / dlDuration.Seconds() / (1024 * 1024)

	// Upload test
	ulStart := time.Now()
	body := bytes.NewReader(make([]byte, testBytes))
	resp, err = client.Post(base+"/api/v1/speedtest/upload", "application/octet-stream", body)
	if err != nil {
		return speedTestMsg{result: &speedTestResult{
			downloadMBps: downloadMBps, proxyURL: proxyURL, testBytes: testBytes,
			err: fmt.Errorf("upload failed: %w", err),
		}}
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	ulDuration := time.Since(ulStart)
	uploadMBps := float64(testBytes) / ulDuration.Seconds() / (1024 * 1024)

	return speedTestMsg{result: &speedTestResult{
		downloadMBps: downloadMBps,
		uploadMBps:   uploadMBps,
		proxyURL:     proxyURL,
		testBytes:    testBytes,
	}}
}

func (pd *PeerDetailView) View() string {
	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  Peer Detail"))
	b.WriteString("\n\n")

	if !pd.ready {
		b.WriteString(dimStyle.Render("  Loading..."))
		return b.String()
	}

	if pd.deleted {
		b.WriteString(successStyle.Render("  Peer deleted successfully."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  esc back to peers"))
		return b.String()
	}

	if pd.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", pd.err)))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  esc back"))
		return b.String()
	}

	if pd.peer == nil {
		b.WriteString(errorStyle.Render("  Peer not found"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  esc back"))
		return b.String()
	}

	p := pd.peer

	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Name:"), valueStyle.Render(p.Name)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Peer ID:"), valueStyle.Render(p.ID)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Machine ID:"), valueStyle.Render(p.MachineID)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Role:"), valueStyle.Render(p.Role)))
	if p.Address != "" {
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Address:"), valueStyle.Render(p.Address)))
	}
	if len(p.Capabilities) > 0 {
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Capabilities:"), valueStyle.Render(strings.Join(p.Capabilities, ", "))))
	}
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Registered:"), valueStyle.Render(p.RegisteredAt.Format("2006-01-02 15:04:05"))))

	lastSeenStr := p.LastSeen.Format("2006-01-02 15:04:05")
	age := time.Since(p.LastSeen)
	ageStyle := successStyle
	switch {
	case age > time.Hour:
		ageStyle = errorStyle
		lastSeenStr += fmt.Sprintf(" (%s ago)", formatDuration(age))
	case age > 10*time.Minute:
		ageStyle = warnStyle
		lastSeenStr += fmt.Sprintf(" (%s ago)", formatDuration(age))
	default:
		lastSeenStr += " (active)"
	}
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Last Seen:"), ageStyle.Render(lastSeenStr)))

	queueLabel := "Queue Size:"
	if pd.isListener && pd.cfg.Role == config.RoleSender {
		queueLabel = "Pending Results:"
	}
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render(queueLabel), valueStyle.Render(fmt.Sprintf("%d", pd.queueSize))))

	if pd.statusMsg != "" {
		b.WriteString("\n")
		b.WriteString(successStyle.Render("  " + pd.statusMsg))
	}

	b.WriteString("\n\n")

	for i, action := range pd.actions {
		cursor := "  "
		style := normalStyle
		label := action
		if i == pd.actionIdx {
			cursor = "> "
			style = selectedStyle
		}
		if action == "Speed Test" && pd.speedTestRunning {
			label = "Speed Test  (running...)"
		}
		b.WriteString(style.Render(cursor + label))
		b.WriteString("\n")
	}

	if pd.speedTestResult != nil {
		b.WriteString("\n")
		r := pd.speedTestResult
		via := r.proxyURL
		if via == "" || via == "direct" {
			via = "direct (no proxy)"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Via:"), valueStyle.Render(via)))
		if r.err != nil {
			b.WriteString(fmt.Sprintf("  %s\n", errorStyle.Render("  Error: "+r.err.Error())))
		} else {
			mb := float64(r.testBytes) / (1024 * 1024)
			b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Download:"),
				successStyle.Render(fmt.Sprintf("%.2f MB/s  (%.0f MB)", r.downloadMBps, mb))))
			b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Upload:"),
				successStyle.Render(fmt.Sprintf("%.2f MB/s  (%.0f MB)", r.uploadMBps, mb))))
		}
	}

	if pd.confirm {
		b.WriteString("\n")
		switch pd.confirmAction {
		case "clear":
			b.WriteString(warnStyle.Render("  Clear queued requests for this peer? (y/n)"))
		case "delete":
			b.WriteString(errorStyle.Render("  Delete this peer? (y/n)"))
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  up/down navigate • enter select • esc back"))

	return b.String()
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
