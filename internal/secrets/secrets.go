// Package secrets provides at-rest encryption for credentials SAK
// stores locally. An OS keychain isn't an option here — SAK's primary
// deployment target is a headless Docker container, which has no desktop
// session and therefore no keychain daemon to talk to. Instead, a locally
// generated master key encrypts every secret with AES-256-GCM before it
// reaches SQLite, so the database file alone never reveals a credential.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keySize = 32 // AES-256

// LoadOrCreateKey reads a 32-byte key from path, generating and writing one
// (mode 0600) if the file doesn't exist yet.
//
// The key is only as safe as this file's permissions and wherever it ends
// up backed up — a sak.db backup without the matching key file is just
// ciphertext, and a key file backed up separately from sak.db defeats
// the point. They travel together.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != keySize {
			return nil, fmt.Errorf("secrets: key file %s is %d bytes, want %d — refusing to use a corrupt key", path, len(key), keySize)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading key file: %w", err)
	}

	key = make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating key directory: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("writing key file: %w", err)
	}
	return key, nil
}

// Store encrypts and decrypts secret values with a single AES-256-GCM key.
type Store struct {
	gcm cipher.AEAD
}

// New builds a Store from a 32-byte key (see LoadOrCreateKey).
func New(key []byte) (*Store, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	return &Store{gcm: gcm}, nil
}

// Encrypt returns plaintext encrypted and base64-encoded, safe to store in a
// SQLite TEXT column. Each call uses a fresh random nonce, prepended to the
// ciphertext, so encrypting the same plaintext twice never produces the same
// output.
func (s *Store) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	ciphertext := s.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt. Fails if encoded is malformed or the
// authentication tag doesn't match — wrong key, or tampered/corrupted data.
func (s *Store) Decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decoding ciphertext: %w", err)
	}
	nonceSize := s.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("secrets: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := s.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting (wrong key or corrupted data): %w", err)
	}
	return string(plaintext), nil
}
