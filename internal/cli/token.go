package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "(Listener) Manage API tokens for relay request authentication",
}

var tokenCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new API token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, rdb, err := loadListenerConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "token")
		name := args[0]

		// Generate token
		plainToken, tokenHash := store.GenerateAPIToken()

		// Generate token ID
		idBytes := make([]byte, 8)
		rand.Read(idBytes)
		tokenID := hex.EncodeToString(idBytes)

		apiToken := &models.APIToken{
			ID:        tokenID,
			Name:      name,
			TokenHash: tokenHash,
			CreatedAt: time.Now(),
		}

		if err := rdb.StoreAPIToken(ctx, apiToken); err != nil {
			return fmt.Errorf("store token: %w", err)
		}

		_ = cfg
		fmt.Println("\n=== API Token Created ===")
		fmt.Printf("Name:  %s\n", name)
		fmt.Printf("ID:    %s\n", tokenID)
		fmt.Printf("Token: %s\n\n", plainToken)
		fmt.Println("Save this token now — it will not be shown again.")
		fmt.Println("Use it in requests: Authorization: Bearer <token>")
		return nil
	},
}

var tokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all API tokens",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, rdb, err := loadListenerConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "token")
		tokens, err := rdb.ListAPITokens(ctx)
		if err != nil {
			return err
		}

		if len(tokens) == 0 {
			fmt.Println("No API tokens configured.")
			fmt.Println("API endpoints are currently open (no auth required).")
			fmt.Println("\nCreate one with: relayra token create <name>")
			return nil
		}

		fmt.Printf("API Tokens (%d):\n\n", len(tokens))
		for i, t := range tokens {
			fmt.Printf("  %d. %s\n", i+1, t.Name)
			fmt.Printf("     ID: %s\n", t.ID)
			fmt.Printf("     Created: %s\n", t.CreatedAt.Format("2006-01-02 15:04:05"))
			if !t.LastUsed.IsZero() {
				fmt.Printf("     Last Used: %s\n", t.LastUsed.Format("2006-01-02 15:04:05"))
			}
			fmt.Printf("     Usage Count: %d\n", t.UsageCount)
			fmt.Println()
		}
		return nil
	},
}

var tokenRevokeCmd = &cobra.Command{
	Use:   "revoke [id]",
	Short: "Revoke (delete) an API token by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, rdb, err := loadListenerConfig()
		if err != nil {
			return err
		}
		defer rdb.Close()

		ctx := logger.WithComponent(context.Background(), "token")
		if err := rdb.DeleteAPIToken(ctx, args[0]); err != nil {
			return err
		}

		fmt.Printf("Token %s revoked.\n", args[0])
		return nil
	},
}

func init() {
	tokenCmd.AddCommand(tokenCreateCmd)
	tokenCmd.AddCommand(tokenListCmd)
	tokenCmd.AddCommand(tokenRevokeCmd)
}

func loadListenerConfig() (*config.Config, store.Backend, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}

	if cfg.Role != config.RoleListener {
		return nil, nil, fmt.Errorf("token management is only available on Listener instances (current role: %s)", cfg.Role)
	}

	logger.SetupStdoutOnly(logger.ParseLevel(cfg.LogLevel))

	rdb, err := store.Open(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("open storage backend: %w", err)
	}

	return cfg, rdb, nil
}
