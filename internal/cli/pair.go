package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/crypto"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/proxy"
	"github.com/relayra/relayra/internal/store"
	"github.com/spf13/cobra"
)

var pairExpires string

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Manage peer pairing",
}

var pairGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "(Listener) Generate a one-time pairing token",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if cfg.Role != config.RoleListener {
			return fmt.Errorf("pair generate is only available on Listener instances")
		}

		logger.SetupStdoutOnly(logger.ParseLevel(cfg.LogLevel))
		ctx := logger.WithComponent(context.Background(), "pairing")

		rdb, err := store.NewRedis(cfg.RedisURL(), cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			return fmt.Errorf("connect to Redis: %w", err)
		}
		defer rdb.Close()

		// Parse expiry duration
		expiry, err := time.ParseDuration(pairExpires)
		if err != nil {
			return fmt.Errorf("invalid expiry duration '%s': %w", pairExpires, err)
		}

		// Generate secret
		secret, err := crypto.GenerateSecret()
		if err != nil {
			return fmt.Errorf("generate secret: %w", err)
		}

		token := &models.PairingToken{
			ListenerAddr: cfg.PublicAddress(),
			ListenerID:   cfg.MachineID,
			ListenerName: cfg.InstanceName,
			Secret:       secret,
			ExpiresAt:    time.Now().Add(expiry).Unix(),
		}

		// Store in Redis
		secretHash := crypto.HashSecret(secret)
		if err := rdb.StorePairingToken(ctx, secretHash, token, expiry); err != nil {
			return fmt.Errorf("store pairing token: %w", err)
		}

		// Encode token for user
		tokenJSON, _ := json.Marshal(token)
		encoded := base64.URLEncoding.EncodeToString(tokenJSON)

		slog.InfoContext(ctx, "pairing token generated",
			"expires_in", expiry,
			"listener_addr", cfg.PublicAddress(),
		)

		fmt.Println("\n=== Pairing Token ===")
		fmt.Printf("Expires: %s\n", time.Now().Add(expiry).Format(time.RFC3339))
		fmt.Printf("\nCopy this token to the Sender server:\n\n%s\n\n", encoded)
		return nil
	},
}

var pairConnectCmd = &cobra.Command{
	Use:   "connect [token]",
	Short: "(Sender) Connect to a Listener using a pairing token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if cfg.Role != config.RoleSender {
			return fmt.Errorf("pair connect is only available on Sender instances")
		}

		logger.SetupStdoutOnly(logger.ParseLevel(cfg.LogLevel))
		ctx := logger.WithComponent(context.Background(), "pairing")

		rdb, err := store.NewRedis(cfg.RedisURL(), cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			return fmt.Errorf("connect to Redis: %w", err)
		}
		defer rdb.Close()

		// Decode token
		tokenJSON, err := base64.URLEncoding.DecodeString(args[0])
		if err != nil {
			return fmt.Errorf("invalid token format: %w", err)
		}

		var token models.PairingToken
		if err := json.Unmarshal(tokenJSON, &token); err != nil {
			return fmt.Errorf("invalid token data: %w", err)
		}

		// Check expiry
		if time.Now().Unix() > token.ExpiresAt {
			return fmt.Errorf("pairing token has expired")
		}

		slog.InfoContext(ctx, "decoded pairing token",
			"listener_addr", token.ListenerAddr,
			"listener_name", token.ListenerName,
			"expires_at", time.Unix(token.ExpiresAt, 0).Format(time.RFC3339),
		)

		fmt.Printf("Connecting to Listener '%s' at %s...\n", token.ListenerName, token.ListenerAddr)

		// Get proxy transport
		proxyMgr := proxy.NewManager(rdb)
		transport, proxyURL, err := proxyMgr.GetTransport(ctx)
		if err != nil {
			slog.WarnContext(ctx, "no proxy available, trying direct connection", "error", err)
			transport = http.DefaultTransport
			proxyURL = "direct"
		}

		slog.InfoContext(ctx, "using transport", "proxy", proxyURL)

		// Send pairing request
		pairReq := &models.PairingRequest{
			Secret:    token.Secret,
			MachineID: cfg.MachineID,
			Name:      cfg.InstanceName,
		}

		pairJSON, _ := json.Marshal(pairReq)
		url := fmt.Sprintf("http://%s/api/v1/pair", token.ListenerAddr)

		slog.InfoContext(ctx, "sending pairing request", "url", url)

		client := &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		}

		resp, err := client.Post(url, "application/json", bytes.NewReader(pairJSON))
		if err != nil {
			slog.ErrorContext(ctx, "pairing request failed", "error", err, "proxy", proxyURL)
			return fmt.Errorf("pairing request failed: %w\nCheck proxy configuration and Listener availability.", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			slog.ErrorContext(ctx, "pairing rejected",
				"status", resp.StatusCode,
				"body", string(body),
			)
			return fmt.Errorf("pairing rejected (HTTP %d): %s", resp.StatusCode, string(body))
		}

		var pairResp models.PairingResponse
		if err := json.Unmarshal(body, &pairResp); err != nil {
			return fmt.Errorf("invalid pairing response: %w", err)
		}

		if !pairResp.Success {
			return fmt.Errorf("pairing failed: %s", pairResp.Error)
		}

		// Derive encryption key
		encKey, err := crypto.DeriveKey(token.Secret, cfg.MachineID, pairResp.MachineID)
		if err != nil {
			return fmt.Errorf("derive encryption key: %w", err)
		}

		// Store Listener info
		listener := &models.Peer{
			ID:            pairResp.PeerID, // Our peer ID assigned by Listener
			Name:          pairResp.ListenerName,
			MachineID:     pairResp.MachineID,
			Address:       token.ListenerAddr,
			EncryptionKey: encKey,
			RegisteredAt:  time.Now(),
		}

		if err := rdb.StoreListenerInfo(ctx, listener); err != nil {
			return fmt.Errorf("store listener info: %w", err)
		}

		slog.InfoContext(ctx, "pairing successful",
			"listener_name", pairResp.ListenerName,
			"peer_id", pairResp.PeerID,
		)

		fmt.Printf("\nPairing successful!\n")
		fmt.Printf("Connected to Listener: %s (%s)\n", pairResp.ListenerName, token.ListenerAddr)
		fmt.Printf("Your Peer ID: %s\n", pairResp.PeerID)
		fmt.Printf("Encryption: AES-256-GCM (key derived)\n")
		return nil
	},
}

func init() {
	pairGenerateCmd.Flags().StringVar(&pairExpires, "expires", "1h", "Token expiry duration (e.g., 30m, 1h, 24h)")
	pairCmd.AddCommand(pairGenerateCmd)
	pairCmd.AddCommand(pairConnectCmd)
}
