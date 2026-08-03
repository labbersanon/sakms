package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKey_GeneratesNewKeyWithCorrectPermissions(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")

	key, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != keySize {
		t.Fatalf("expected a %d-byte key, got %d", keySize, len(key))
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected key file to exist: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected mode 0600, got %o", perm)
	}
}

func TestLoadOrCreateKey_LoadsExistingKeyUnchanged(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")

	first, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("unexpected error on second load: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("expected the same key to be loaded on a second call, got a different one")
	}
}

func TestLoadOrCreateKey_RejectsCorruptKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(keyPath, []byte("too short"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := LoadOrCreateKey(keyPath); err == nil {
		t.Fatal("expected an error for a key file of the wrong size")
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, keySize)
	store, err := New(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	plaintext := "sk-super-secret-api-key-12345"
	encrypted, err := store.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("unexpected error encrypting: %v", err)
	}
	if encrypted == plaintext {
		t.Fatal("encrypted value should not equal the plaintext")
	}

	decrypted, err := store.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("unexpected error decrypting: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestEncrypt_DifferentEachTime(t *testing.T) {
	store, _ := New(make([]byte, keySize))
	a, _ := store.Encrypt("same plaintext")
	b, _ := store.Encrypt("same plaintext")
	if a == b {
		t.Fatal("expected two encryptions of the same plaintext to differ (random nonce), got identical ciphertext")
	}
}

func TestDecrypt_FailsWithWrongKey(t *testing.T) {
	storeA, _ := New(make([]byte, keySize))
	keyB := make([]byte, keySize)
	keyB[0] = 1 // different from storeA's all-zero key
	storeB, _ := New(keyB)

	encrypted, err := storeA.Encrypt("secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := storeB.Decrypt(encrypted); err == nil {
		t.Fatal("expected decryption with the wrong key to fail")
	}
}

func TestDecrypt_FailsOnTamperedCiphertext(t *testing.T) {
	store, _ := New(make([]byte, keySize))
	encrypted, err := store.Encrypt("secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tampered := []byte(encrypted)
	tampered[len(tampered)-1] ^= 0xFF // flip the last base64 char's underlying bits
	if _, err := store.Decrypt(string(tampered)); err == nil {
		t.Fatal("expected decryption of tampered ciphertext to fail")
	}
}

func TestDecrypt_FailsOnMalformedInput(t *testing.T) {
	store, _ := New(make([]byte, keySize))
	if _, err := store.Decrypt("not valid base64!!!"); err == nil {
		t.Fatal("expected an error for malformed input")
	}
}

func TestNew_RejectsWrongKeySize(t *testing.T) {
	if _, err := New([]byte("too short")); err == nil {
		t.Fatal("expected an error for a key that isn't 32 bytes")
	}
}

// The AAD pair round-trips, and encrypting the same plaintext twice under
// the same AAD still produces different ciphertext (fresh nonce per call).
func TestEncryptWithAAD_RoundTrip(t *testing.T) {
	store, _ := New(make([]byte, keySize))
	const plaintext = "sakms-unlock-ticket-payload"
	aad := []byte("sakms-section-unlock-v1")

	first, err := store.EncryptWithAAD(plaintext, aad)
	if err != nil {
		t.Fatalf("EncryptWithAAD: %v", err)
	}
	second, err := store.EncryptWithAAD(plaintext, aad)
	if err != nil {
		t.Fatalf("EncryptWithAAD: %v", err)
	}
	if first == second {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext — the nonce is not fresh")
	}
	got, err := store.DecryptWithAAD(first, aad)
	if err != nil {
		t.Fatalf("DecryptWithAAD: %v", err)
	}
	if got != plaintext {
		t.Fatalf("round-trip = %q, want %q", got, plaintext)
	}
}

// The domain separation, asserted in every direction that matters. This is
// what makes the unlock ticket non-interchangeable with a session cookie:
// the two families are mutually undecryptable at the crypto layer, before
// any payload field is read.
func TestAADDomainSeparation(t *testing.T) {
	store, _ := New(make([]byte, keySize))
	const plaintext = "same payload, two domains"
	domainA := []byte("sakms-section-unlock-v1")
	domainB := []byte("sakms-some-other-domain")

	sealedA, err := store.EncryptWithAAD(plaintext, domainA)
	if err != nil {
		t.Fatalf("EncryptWithAAD: %v", err)
	}
	sealedNil, err := store.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if _, err := store.DecryptWithAAD(sealedA, domainB); err == nil {
		t.Error("a ciphertext sealed under domain A decrypted under domain B")
	}
	if _, err := store.Decrypt(sealedA); err == nil {
		t.Error("a ciphertext sealed under an AAD decrypted with the nil-AAD Decrypt — " +
			"an unlock ticket would be usable as a session cookie")
	}
	if _, err := store.DecryptWithAAD(sealedNil, domainA); err == nil {
		t.Error("a nil-AAD ciphertext decrypted under an AAD — " +
			"a session cookie would be usable as an unlock ticket")
	}
	// A nil AAD passed explicitly is the same domain the existing pair uses,
	// so the two forms stay interoperable for callers that want one code path.
	if got, err := store.DecryptWithAAD(sealedNil, nil); err != nil || got != plaintext {
		t.Errorf("DecryptWithAAD(_, nil) = %q, %v; want the plaintext and no error", got, err)
	}
}
