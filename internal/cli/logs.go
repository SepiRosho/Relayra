package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	"github.com/spf13/cobra"
)

var (
	logsTail   int
	logsFollow bool
	logsLevel  string
	logsGrep   string
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View Relayra logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		logDir := cfg.LogDir
		if logDir == "" {
			logDir = "/opt/relayra/logs"
		}

		// Find log files
		files, err := filepath.Glob(filepath.Join(logDir, "relayra-*.log"))
		if err != nil || len(files) == 0 {
			return fmt.Errorf("no log files found in %s", logDir)
		}

		sort.Strings(files)

		if logsFollow {
			// Tail -f mode on the latest log file
			return tailFollow(files[len(files)-1])
		}

		// Read and filter the latest log file
		latestFile := files[len(files)-1]
		return readLogFile(latestFile, logsTail, logsLevel, logsGrep)
	},
}

func init() {
	logsCmd.Flags().IntVar(&logsTail, "tail", 50, "Number of lines to show")
	logsCmd.Flags().BoolVar(&logsFollow, "follow", false, "Follow log output (like tail -f)")
	logsCmd.Flags().StringVar(&logsLevel, "level", "", "Filter by log level (debug, info, warn, error)")
	logsCmd.Flags().StringVar(&logsGrep, "grep", "", "Filter by keyword (e.g., a request_id or peer_id)")
	_ = logger.ParseLevel // suppress unused
}

func readLogFile(path string, tailN int, level, grep string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	// Increase buffer size for long log lines
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if matchesFilter(line, level, grep) {
			lines = append(lines, line)
		}
	}

	// Show last N lines
	start := 0
	if tailN > 0 && len(lines) > tailN {
		start = len(lines) - tailN
	}

	fmt.Printf("=== %s (showing %d of %d matching lines) ===\n\n", filepath.Base(path), len(lines)-start, len(lines))
	for _, line := range lines[start:] {
		fmt.Println(line)
	}
	return nil
}

func tailFollow(path string) error {
	fmt.Printf("=== Following %s (Ctrl+C to stop) ===\n\n", filepath.Base(path))

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	// Seek to end
	f.Seek(0, 2)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for {
		for scanner.Scan() {
			line := scanner.Text()
			if matchesFilter(line, logsLevel, logsGrep) {
				fmt.Println(line)
			}
		}
		// Scanner hit EOF, wait and retry
		// Note: This is a simple approach. For production, use fsnotify.
		// But for our use case (manual log watching), this is fine.
		scanner = bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	}
}

func matchesFilter(line, level, grep string) bool {
	if level != "" {
		levelUpper := strings.ToUpper(level)
		if !strings.Contains(strings.ToUpper(line), levelUpper) {
			return false
		}
	}
	if grep != "" {
		if !strings.Contains(line, grep) {
			return false
		}
	}
	return true
}
