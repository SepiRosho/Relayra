package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
)

// GenerateMachineID creates a unique SHA256 identifier for this machine
// using hostname + first MAC address + random salt. The salt ensures uniqueness
// even if hostname/MAC are identical (e.g., cloned VMs).
func GenerateMachineID() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	macAddr := getFirstMAC()

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate random salt: %w", err)
	}

	data := fmt.Sprintf("%s|%s|%s", hostname, macAddr, hex.EncodeToString(salt))
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:]), nil
}

func getFirstMAC() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "no-mac"
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) > 0 {
			return iface.HardwareAddr.String()
		}
	}
	return "no-mac"
}
