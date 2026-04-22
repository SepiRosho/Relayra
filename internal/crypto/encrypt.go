package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	// maxTimestampDrift is the maximum allowed clock drift for replay protection.
	maxTimestampDrift = 5 * time.Minute
)

// randReader returns crypto/rand.Reader. Extracted for testing.
func randReader() io.Reader {
	return rand.Reader
}

// Encrypt encrypts data using AES-256-GCM with a random nonce and prepended timestamp.
// Returns base64-encoded ciphertext and the base64-encoded nonce.
func Encrypt(key []byte, plaintext []byte) (ciphertext string, nonce string, timestamp int64, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", 0, fmt.Errorf("create AES cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", 0, fmt.Errorf("create GCM: %w", err)
	}

	nonceBytes := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(randReader(), nonceBytes); err != nil {
		return "", "", 0, fmt.Errorf("generate nonce: %w", err)
	}

	ts := time.Now().Unix()

	// Prepend timestamp to plaintext as additional authenticated data
	tsBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBytes, uint64(ts))

	encrypted := aesGCM.Seal(nil, nonceBytes, plaintext, tsBytes)

	return base64.StdEncoding.EncodeToString(encrypted),
		base64.StdEncoding.EncodeToString(nonceBytes),
		ts,
		nil
}

// Decrypt decrypts AES-256-GCM encrypted data and validates the timestamp.
func Decrypt(key []byte, ciphertextB64, nonceB64 string, timestamp int64) ([]byte, error) {
	// Validate timestamp for replay protection
	now := time.Now().Unix()
	drift := now - timestamp
	if drift < 0 {
		drift = -drift
	}
	if drift > int64(maxTimestampDrift.Seconds()) {
		return nil, fmt.Errorf("timestamp drift too large: %d seconds (max %v)", drift, maxTimestampDrift)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	nonceBytes, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	// Reconstruct AAD from timestamp
	tsBytes := make([]byte, 8)
	if timestamp >= 0 && uint64(timestamp) <= math.MaxUint64 {
		binary.BigEndian.PutUint64(tsBytes, uint64(timestamp))
	}

	plaintext, err := aesGCM.Open(nil, nonceBytes, ciphertext, tsBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w (possible key mismatch or tampered data)", err)
	}

	return plaintext, nil
}

// EncryptJSON marshals v to JSON and encrypts it.
func EncryptJSON(key []byte, v any) (ciphertext, nonce string, timestamp int64, err error) {
	plaintext, err := json.Marshal(v)
	if err != nil {
		return "", "", 0, fmt.Errorf("marshal for encryption: %w", err)
	}
	return Encrypt(key, plaintext)
}

// DecryptJSON decrypts and unmarshals into v.
func DecryptJSON(key []byte, ciphertext, nonce string, timestamp int64, v any) error {
	plaintext, err := Decrypt(key, ciphertext, nonce, timestamp)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(plaintext, v); err != nil {
		return fmt.Errorf("unmarshal decrypted data: %w", err)
	}
	return nil
}
