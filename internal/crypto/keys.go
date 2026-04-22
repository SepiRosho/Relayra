package crypto

import (
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// DeriveKey derives a 32-byte AES-256 key from a shared secret and both machine IDs
// using HKDF-SHA256. This ensures both sides derive the same key deterministically.
func DeriveKey(sharedSecret string, machineID1, machineID2 string) ([]byte, error) {
	// Sort machine IDs to ensure same key regardless of which side derives it
	id1, id2 := machineID1, machineID2
	if id1 > id2 {
		id1, id2 = id2, id1
	}

	salt := []byte(fmt.Sprintf("relayra-v1|%s|%s", id1, id2))
	info := []byte("relayra-aes256-gcm")

	hkdfReader := hkdf.New(sha256.New, []byte(sharedSecret), salt, info)

	key := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("HKDF key derivation failed: %w", err)
	}

	return key, nil
}

// GenerateSecret generates a cryptographically random secret for pairing.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(randReader(), b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}

	// Encode as hex string
	return fmt.Sprintf("%x", b), nil
}

// HashSecret creates a SHA-256 hash of a secret for storage indexing.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return fmt.Sprintf("%x", h[:])
}
