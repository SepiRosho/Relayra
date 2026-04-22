package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/store"
)

// TokensView manages API tokens.
type TokensView struct {
	rdb        store.Backend
	tokens     []*models.APIToken
	cursor     int
	err        error
	ready      bool
	creating   bool
	nameInput  string
	confirm    bool
	confirmIdx int
	created    *createdTokenInfo
}

type createdTokenInfo struct {
	Name  string
	Token string
}

type tokensLoadedMsg struct {
	tokens []*models.APIToken
	err    error
}

type tokenCreatedMsg struct {
	name  string
	token string
	err   error
}

type tokenDeletedMsg struct {
	err error
}

// NewTokensView creates a new API tokens management view.
func NewTokensView(rdb store.Backend) *TokensView {
	return &TokensView{rdb: rdb}
}

func (tv *TokensView) Init() tea.Cmd {
	return tv.loadTokens
}

func (tv *TokensView) loadTokens() tea.Msg {
	tokens, err := tv.rdb.ListAPITokens(context.Background())
	return tokensLoadedMsg{tokens: tokens, err: err}
}

func (tv *TokensView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tokensLoadedMsg:
		tv.tokens = msg.tokens
		tv.err = msg.err
		tv.ready = true
		return tv, nil

	case tokenCreatedMsg:
		if msg.err != nil {
			tv.err = msg.err
		} else {
			tv.created = &createdTokenInfo{Name: msg.name, Token: msg.token}
		}
		tv.creating = false
		return tv, tv.loadTokens

	case tokenDeletedMsg:
		if msg.err != nil {
			tv.err = msg.err
		}
		tv.confirm = false
		return tv, tv.loadTokens

	case tea.KeyMsg:
		// Show created token — press any key to dismiss
		if tv.created != nil {
			tv.created = nil
			return tv, nil
		}

		if tv.creating {
			return tv.updateCreating(msg)
		}

		if tv.confirm {
			switch msg.String() {
			case "y", "Y":
				idx := tv.confirmIdx
				tv.confirm = false
				if idx < len(tv.tokens) {
					tokenID := tv.tokens[idx].ID
					return tv, func() tea.Msg {
						err := tv.rdb.DeleteAPIToken(context.Background(), tokenID)
						return tokenDeletedMsg{err: err}
					}
				}
			default:
				tv.confirm = false
			}
			return tv, nil
		}

		switch msg.String() {
		case "up", "k":
			if tv.cursor > 0 {
				tv.cursor--
			}
		case "down", "j":
			if tv.cursor < len(tv.tokens)-1 {
				tv.cursor++
			}
		case "c":
			tv.creating = true
			tv.nameInput = ""
		case "d":
			if len(tv.tokens) > 0 && tv.cursor < len(tv.tokens) {
				tv.confirm = true
				tv.confirmIdx = tv.cursor
			}
		case "r":
			tv.ready = false
			return tv, tv.loadTokens
		}
	}
	return tv, nil
}

func (tv *TokensView) updateCreating(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		tv.creating = false
		return tv, nil
	case "enter":
		name := strings.TrimSpace(tv.nameInput)
		if name == "" {
			return tv, nil
		}
		tv.creating = false
		return tv, func() tea.Msg {
			plainToken, tokenHash := store.GenerateAPIToken()
			idBytes := make([]byte, 8)
			rand.Read(idBytes)
			tokenID := hex.EncodeToString(idBytes)

			apiToken := &models.APIToken{
				ID:        tokenID,
				Name:      name,
				TokenHash: tokenHash,
				CreatedAt: time.Now(),
			}

			err := tv.rdb.StoreAPIToken(context.Background(), apiToken)
			return tokenCreatedMsg{name: name, token: plainToken, err: err}
		}
	case "backspace":
		if len(tv.nameInput) > 0 {
			tv.nameInput = tv.nameInput[:len(tv.nameInput)-1]
		}
	default:
		if len(msg.String()) == 1 && len(tv.nameInput) < 32 {
			tv.nameInput += msg.String()
		}
	}
	return tv, nil
}

func (tv *TokensView) View() string {
	var b strings.Builder

	b.WriteString(subtitleStyle.Render("  API Tokens"))
	b.WriteString("\n\n")

	if !tv.ready {
		b.WriteString(dimStyle.Render("  Loading..."))
		return b.String()
	}

	if tv.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", tv.err)))
		b.WriteString("\n\n")
	}

	// Show created token
	if tv.created != nil {
		b.WriteString(successStyle.Render("  Token created successfully!"))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Name:"), valueStyle.Render(tv.created.Name)))
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Token:"), selectedStyle.Render(tv.created.Token)))
		b.WriteString("\n")
		b.WriteString(warnStyle.Render("  Save this token now — it will not be shown again."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  Press any key to continue"))
		return b.String()
	}

	// Creating
	if tv.creating {
		b.WriteString(normalStyle.Render("  Enter token name:"))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("  %s%s\n", selectedStyle.Render(tv.nameInput), dimStyle.Render("█")))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  enter create • esc cancel"))
		return b.String()
	}

	// Confirm delete
	if tv.confirm && tv.confirmIdx < len(tv.tokens) {
		b.WriteString(warnStyle.Render(fmt.Sprintf("  Revoke token '%s'? (y/N)", tv.tokens[tv.confirmIdx].Name)))
		b.WriteString("\n")
		return b.String()
	}

	if len(tv.tokens) == 0 {
		b.WriteString(dimStyle.Render("  No API tokens configured."))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  API endpoints are currently open (no auth required)."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  Press 'c' to create your first token."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("  c create • esc back"))
		return b.String()
	}

	// Token list
	header := fmt.Sprintf("  %-20s  %-16s  %-20s  %-6s", "Name", "ID", "Last Used", "Uses")
	b.WriteString(dimStyle.Render(header))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  " + strings.Repeat("─", 68)))
	b.WriteString("\n")

	for i, t := range tv.tokens {
		cursor := "  "
		style := normalStyle
		if i == tv.cursor {
			cursor = "▸ "
			style = selectedStyle
		}

		lastUsed := "never"
		if !t.LastUsed.IsZero() {
			lastUsed = t.LastUsed.Format("2006-01-02 15:04")
		}

		line := fmt.Sprintf("%s%-20s  %-16s  %-20s  %-6d",
			cursor,
			truncate(t.Name, 20),
			t.ID,
			lastUsed,
			t.UsageCount,
		)
		b.WriteString(style.Render(line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  c create • d revoke • r refresh • ↑/↓ navigate • esc back"))

	return b.String()
}
