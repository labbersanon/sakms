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
// up backed up — a sakms.db backup without the matching key file is just
// ciphertext, and a key file backed up separately from sakms.db defeats
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

// EncryptWithAAD is Encrypt with additional authenticated data bound into
// the ciphertext's authentication tag. Same key, same AES-256-GCM
// primitive, same output shape — aad is authenticated but not stored, so
// the caller must present the identical value to DecryptWithAAD.
//
// It exists for DOMAIN SEPARATION. Encrypt/Decrypt seal with a nil AAD, so
// every ciphertext in the app shares one namespace under one key: a value
// of one type is interchangeable with a value of another type that happens
// to share its payload shape. An AAD makes two ciphertext families
// mutually undecryptable at the crypto layer, before any field is read.
//
// This is deliberately a NEW method rather than an AAD parameter added to
// Encrypt. The existing pair holds every already-stored ciphertext across
// eight packages (session cookies, the OIDC client secret, grabs, RSS
// feeds, connections, service connections, Trakt, webhooks); giving them a
// non-nil AAD would invalidate all of it at once.
func (s *Store) EncryptWithAAD(plaintext string, aad []byte) (string, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	ciphertext := s.gcm.Seal(nonce, nonce, []byte(plaintext), aad)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptWithAAD reverses EncryptWithAAD. Fails if encoded is malformed,
// if the key is wrong, if the data was tampered with — or if aad differs
// by even one byte from the value it was sealed under, which is the whole
// point (see EncryptWithAAD).
func (s *Store) DecryptWithAAD(encoded string, aad []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decoding ciphertext: %w", err)
	}
	nonceSize := s.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("secrets: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := s.gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("decrypting (wrong key, wrong domain, or corrupted data): %w", err)
	}
	return string(plaintext), nil
}
