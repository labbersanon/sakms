package sectionlock

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/labbersanon/sakms/internal/settings"
)

// fakeSettings is an in-memory settings KV with injectable read failures.
//
// It counts Get calls per key, because "retries once then fails closed"
// cannot be verified from the returned error alone — an implementation
// that never retries produces the identical error.
type fakeSettings struct {
	mu         sync.Mutex
	values     map[string]string
	getCalls   map[string]int
	failNext   map[string]int  // fail this many upcoming Gets, then succeed
	failAlways map[string]bool // fail every Get
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{
		values:     map[string]string{},
		getCalls:   map[string]int{},
		failNext:   map[string]int{},
		failAlways: map[string]bool{},
	}
}

var errTransient = errors.New("database is locked")

func (f *fakeSettings) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls[key]++
	if f.failAlways[key] {
		return "", errTransient
	}
	if f.failNext[key] > 0 {
		f.failNext[key]--
		return "", errTransient
	}
	v, ok := f.values[key]
	if !ok {
		return "", settings.ErrNotFound
	}
	return v, nil
}

func (f *fakeSettings) Set(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
	return nil
}

func (f *fakeSettings) calls(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls[key]
}

// testCrypto mirrors internal/secrets.Store's AES-256-GCM scheme exactly —
// random nonce prepended, base64-encoded — and adds the AAD-taking pair
// W1 will implement on the real Store.
//
// It deliberately provides BOTH shapes over ONE key, because that is the
// only configuration in which SL-2's question is meaningful: two credential
// types sharing a key, distinguished by AAD and by payload type.
type testCrypto struct{ gcm cipher.AEAD }

func newTestCrypto(t *testing.T) *testCrypto {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("creating cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("creating GCM: %v", err)
	}
	return &testCrypto{gcm: gcm}
}

func (c *testCrypto) seal(plaintext string, aad []byte) (string, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(c.gcm.Seal(nonce, nonce, []byte(plaintext), aad)), nil
}

func (c *testCrypto) open(encoded string, aad []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	n := c.gcm.NonceSize()
	if len(data) < n {
		return "", errors.New("ciphertext too short")
	}
	out, err := c.gcm.Open(nil, data[:n], data[n:], aad)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (c *testCrypto) EncryptWithAAD(plaintext string, aad []byte) (string, error) {
	return c.seal(plaintext, aad)
}

func (c *testCrypto) DecryptWithAAD(encoded string, aad []byte) (string, error) {
	return c.open(encoded, aad)
}

// Encrypt/Decrypt are the nil-AAD pair — byte-identical in behaviour to
// secrets.Store's shipped methods, and therefore to what auth's session
// cookie is sealed and validated with today.
func (c *testCrypto) Encrypt(plaintext string) (string, error) { return c.seal(plaintext, nil) }
func (c *testCrypto) Decrypt(encoded string) (string, error)   { return c.open(encoded, nil) }

// newTestGate wires a Gate over an empty fake settings store.
func newTestGate(t *testing.T) (*Gate, *fakeSettings) {
	t.Helper()
	fake := newFakeSettings()
	return NewGate(NewStore(fake), newTestCrypto(t)), fake
}
