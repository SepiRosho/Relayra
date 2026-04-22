package config

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Role defines the operational mode of this Relayra instance.
type Role string

const (
	RoleListener Role = "listener"
	RoleSender   Role = "sender"
)

// Config holds all configuration for a Relayra instance.
type Config struct {
	// Auto-generated
	MachineID    string `env:"RELAYRA_MACHINE_ID"`
	InstanceName string `env:"RELAYRA_INSTANCE_NAME"`

	// Role
	Role Role `env:"RELAYRA_ROLE"`

	// Network
	ListenAddr string `env:"RELAYRA_LISTEN_ADDR"`
	ListenPort int    `env:"RELAYRA_LISTEN_PORT"`
	PublicAddr string `env:"RELAYRA_PUBLIC_ADDR"` // External IP for pairing tokens

	// Storage
	StorageBackend string `env:"RELAYRA_STORAGE_BACKEND"`
	SQLitePath     string `env:"RELAYRA_SQLITE_PATH"`

	// Redis
	RedisAddr     string `env:"RELAYRA_REDIS_ADDR"`
	RedisPort     int    `env:"RELAYRA_REDIS_PORT"`
	RedisPassword string `env:"RELAYRA_REDIS_PASSWORD"`
	RedisDB       int    `env:"RELAYRA_REDIS_DB"`

	// Polling (Sender)
	PollInterval   int  `env:"RELAYRA_POLL_INTERVAL"`
	PollBatchSize  int  `env:"RELAYRA_POLL_BATCH_SIZE"`
	RequestTimeout int  `env:"RELAYRA_REQUEST_TIMEOUT"`
	LongPolling    bool `env:"RELAYRA_LONG_POLLING"`
	LongPollWait   int  `env:"RELAYRA_LONG_POLL_WAIT"`
	AsyncWorkers   int  `env:"RELAYRA_ASYNC_WORKERS"`

	// Execution
	AllowListenerExecution bool `env:"RELAYRA_ALLOW_LISTENER_EXECUTION"`

	// Logging
	LogLevel   string `env:"RELAYRA_LOG_LEVEL"`
	LogDir     string `env:"RELAYRA_LOG_DIR"`
	LogMaxDays int    `env:"RELAYRA_LOG_MAX_DAYS"`

	// Results & Webhook
	ResultTTL         int `env:"RELAYRA_RESULT_TTL"`
	WebhookMaxRetries int `env:"RELAYRA_WEBHOOK_MAX_RETRIES"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr:             "0.0.0.0",
		ListenPort:             10000 + rand.Intn(55535),
		StorageBackend:         "redis",
		SQLitePath:             defaultSQLitePath(),
		RedisAddr:              "127.0.0.1",
		RedisPort:              6379,
		RedisPassword:          "",
		RedisDB:                0,
		PollInterval:           5,
		PollBatchSize:          10,
		RequestTimeout:         30,
		LongPolling:            true,
		LongPollWait:           30,
		AsyncWorkers:           4,
		AllowListenerExecution: false,
		LogLevel:               "info",
		LogDir:                 "/opt/relayra/logs",
		LogMaxDays:             7,
		ResultTTL:              86400,
		WebhookMaxRetries:      3,
	}
}

// EnvPath returns the path to the .env file in the Relayra data directory.
func EnvPath() string {
	// Check if running from /opt/relayra (installed)
	if _, err := os.Stat("/opt/relayra"); err == nil {
		return "/opt/relayra/.env"
	}
	// Fallback to current working directory
	dir, _ := os.Getwd()
	return filepath.Join(dir, ".env")
}

// Exists checks if the .env configuration file exists.
func Exists() bool {
	_, err := os.Stat(EnvPath())
	return err == nil
}

// Load reads the .env file and returns a populated Config.
func Load() (*Config, error) {
	envPath := EnvPath()
	if err := godotenv.Load(envPath); err != nil {
		return nil, fmt.Errorf("load .env from %s: %w", envPath, err)
	}

	cfg := DefaultConfig()

	cfg.MachineID = getEnvStr("RELAYRA_MACHINE_ID", "")
	cfg.InstanceName = getEnvStr("RELAYRA_INSTANCE_NAME", "")
	cfg.Role = Role(getEnvStr("RELAYRA_ROLE", ""))
	cfg.ListenAddr = getEnvStr("RELAYRA_LISTEN_ADDR", cfg.ListenAddr)
	cfg.ListenPort = getEnvInt("RELAYRA_LISTEN_PORT", cfg.ListenPort)
	cfg.PublicAddr = getEnvStr("RELAYRA_PUBLIC_ADDR", cfg.PublicAddr)
	cfg.StorageBackend = getEnvStr("RELAYRA_STORAGE_BACKEND", cfg.StorageBackend)
	cfg.SQLitePath = getEnvStr("RELAYRA_SQLITE_PATH", cfg.SQLitePath)
	cfg.RedisAddr = getEnvStr("RELAYRA_REDIS_ADDR", cfg.RedisAddr)
	cfg.RedisPort = getEnvInt("RELAYRA_REDIS_PORT", cfg.RedisPort)
	cfg.RedisPassword = getEnvStr("RELAYRA_REDIS_PASSWORD", cfg.RedisPassword)
	cfg.RedisDB = getEnvInt("RELAYRA_REDIS_DB", cfg.RedisDB)
	cfg.PollInterval = getEnvInt("RELAYRA_POLL_INTERVAL", cfg.PollInterval)
	cfg.PollBatchSize = getEnvInt("RELAYRA_POLL_BATCH_SIZE", cfg.PollBatchSize)
	cfg.RequestTimeout = getEnvInt("RELAYRA_REQUEST_TIMEOUT", cfg.RequestTimeout)
	cfg.LongPolling = getEnvBool("RELAYRA_LONG_POLLING", cfg.LongPolling)
	cfg.LongPollWait = getEnvInt("RELAYRA_LONG_POLL_WAIT", cfg.LongPollWait)
	cfg.AsyncWorkers = getEnvInt("RELAYRA_ASYNC_WORKERS", cfg.AsyncWorkers)
	cfg.AllowListenerExecution = getEnvBool("RELAYRA_ALLOW_LISTENER_EXECUTION", cfg.AllowListenerExecution)
	cfg.LogLevel = getEnvStr("RELAYRA_LOG_LEVEL", cfg.LogLevel)
	cfg.LogDir = getEnvStr("RELAYRA_LOG_DIR", cfg.LogDir)
	cfg.LogMaxDays = getEnvInt("RELAYRA_LOG_MAX_DAYS", cfg.LogMaxDays)
	cfg.ResultTTL = getEnvInt("RELAYRA_RESULT_TTL", cfg.ResultTTL)
	cfg.WebhookMaxRetries = getEnvInt("RELAYRA_WEBHOOK_MAX_RETRIES", cfg.WebhookMaxRetries)

	return cfg, nil
}

// Save writes the Config to the .env file.
func Save(cfg *Config) error {
	envPath := EnvPath()
	dir := filepath.Dir(envPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	lines := []string{
		"# Relayra Configuration",
		"# Auto-generated — edit with care",
		"",
		"# Identity",
		fmt.Sprintf("RELAYRA_MACHINE_ID=%s", cfg.MachineID),
		fmt.Sprintf("RELAYRA_INSTANCE_NAME=%s", cfg.InstanceName),
		"",
		"# Role",
		fmt.Sprintf("RELAYRA_ROLE=%s", cfg.Role),
		"",
		"# Network",
		fmt.Sprintf("RELAYRA_LISTEN_ADDR=%s", cfg.ListenAddr),
		fmt.Sprintf("RELAYRA_LISTEN_PORT=%d", cfg.ListenPort),
		fmt.Sprintf("RELAYRA_PUBLIC_ADDR=%s", cfg.PublicAddr),
		"",
		"# Storage",
		fmt.Sprintf("RELAYRA_STORAGE_BACKEND=%s", cfg.StorageBackend),
		fmt.Sprintf("RELAYRA_SQLITE_PATH=%s", cfg.SQLitePath),
		"",
		"# Redis",
		fmt.Sprintf("RELAYRA_REDIS_ADDR=%s", cfg.RedisAddr),
		fmt.Sprintf("RELAYRA_REDIS_PORT=%d", cfg.RedisPort),
		fmt.Sprintf("RELAYRA_REDIS_PASSWORD=%s", cfg.RedisPassword),
		fmt.Sprintf("RELAYRA_REDIS_DB=%d", cfg.RedisDB),
		"",
		"# Polling (Sender)",
		fmt.Sprintf("RELAYRA_POLL_INTERVAL=%d", cfg.PollInterval),
		fmt.Sprintf("RELAYRA_POLL_BATCH_SIZE=%d", cfg.PollBatchSize),
		fmt.Sprintf("RELAYRA_REQUEST_TIMEOUT=%d", cfg.RequestTimeout),
		fmt.Sprintf("RELAYRA_LONG_POLLING=%t", cfg.LongPolling),
		fmt.Sprintf("RELAYRA_LONG_POLL_WAIT=%d", cfg.LongPollWait),
		fmt.Sprintf("RELAYRA_ASYNC_WORKERS=%d", cfg.AsyncWorkers),
		"",
		"# Execution",
		fmt.Sprintf("RELAYRA_ALLOW_LISTENER_EXECUTION=%t", cfg.AllowListenerExecution),
		"",
		"# Logging",
		fmt.Sprintf("RELAYRA_LOG_LEVEL=%s", cfg.LogLevel),
		fmt.Sprintf("RELAYRA_LOG_DIR=%s", cfg.LogDir),
		fmt.Sprintf("RELAYRA_LOG_MAX_DAYS=%d", cfg.LogMaxDays),
		"",
		"# Results & Webhook",
		fmt.Sprintf("RELAYRA_RESULT_TTL=%d", cfg.ResultTTL),
		fmt.Sprintf("RELAYRA_WEBHOOK_MAX_RETRIES=%d", cfg.WebhookMaxRetries),
		"",
	}

	content := strings.Join(lines, "\n")
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write .env to %s: %w", envPath, err)
	}
	return nil
}

// Validate checks that required fields are present and values are reasonable.
func (c *Config) Validate() error {
	if c.MachineID == "" {
		return fmt.Errorf("RELAYRA_MACHINE_ID is required")
	}
	if c.InstanceName == "" {
		return fmt.Errorf("RELAYRA_INSTANCE_NAME is required")
	}
	if len(c.InstanceName) > 32 {
		return fmt.Errorf("RELAYRA_INSTANCE_NAME must be 32 characters or fewer")
	}
	if c.Role != RoleListener && c.Role != RoleSender {
		return fmt.Errorf("RELAYRA_ROLE must be 'listener' or 'sender', got '%s'", c.Role)
	}
	switch strings.ToLower(strings.TrimSpace(c.StorageBackend)) {
	case "redis", "sqlite":
	default:
		return fmt.Errorf("RELAYRA_STORAGE_BACKEND must be 'redis' or 'sqlite', got '%s'", c.StorageBackend)
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return fmt.Errorf("RELAYRA_LISTEN_PORT must be between 1 and 65535, got %d", c.ListenPort)
	}
	if c.RedisPort < 1 || c.RedisPort > 65535 {
		return fmt.Errorf("RELAYRA_REDIS_PORT must be between 1 and 65535, got %d", c.RedisPort)
	}
	if c.PollInterval < 1 {
		return fmt.Errorf("RELAYRA_POLL_INTERVAL must be >= 1 second, got %d", c.PollInterval)
	}
	if c.PollBatchSize < 1 {
		return fmt.Errorf("RELAYRA_POLL_BATCH_SIZE must be >= 1, got %d", c.PollBatchSize)
	}
	if c.RequestTimeout < 1 {
		return fmt.Errorf("RELAYRA_REQUEST_TIMEOUT must be >= 1 second, got %d", c.RequestTimeout)
	}
	if c.LongPollWait < 1 {
		return fmt.Errorf("RELAYRA_LONG_POLL_WAIT must be >= 1 second, got %d", c.LongPollWait)
	}
	if c.AsyncWorkers < 1 {
		return fmt.Errorf("RELAYRA_ASYNC_WORKERS must be >= 1, got %d", c.AsyncWorkers)
	}
	if c.ResultTTL < 1 {
		return fmt.Errorf("RELAYRA_RESULT_TTL must be >= 1 second, got %d", c.ResultTTL)
	}
	return nil
}

// RedisURL returns the Redis connection string.
func (c *Config) RedisURL() string {
	return fmt.Sprintf("%s:%d", c.RedisAddr, c.RedisPort)
}

// ListenAddress returns the full listen address.
func (c *Config) ListenAddress() string {
	return fmt.Sprintf("%s:%d", c.ListenAddr, c.ListenPort)
}

// PublicAddress returns the externally reachable address for pairing tokens.
// Falls back to ListenAddress if PublicAddr is not set.
func (c *Config) PublicAddress() string {
	if c.PublicAddr != "" {
		return fmt.Sprintf("%s:%d", c.PublicAddr, c.ListenPort)
	}
	return c.ListenAddress()
}

func getEnvStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func defaultSQLitePath() string {
	if _, err := os.Stat("/opt/relayra"); err == nil {
		return "/opt/relayra/relayra.db"
	}
	dir, _ := os.Getwd()
	return filepath.Join(dir, "relayra.db")
}
