package payment

import (
	"crypto/rand"
	"strings"
	"testing"
)

// Historical configuration ciphertext needs round trips across text lengths
// and Unicode while rejecting unusable keys and tested format errors. These
// tests compare restored plaintext and three serialized fields, using fresh
// random keys per test; wrong decoded nonce length is outside this fixture
// set.

// makeKey returns a fresh thirty-two-byte cryptographic key for one test and
// fails that test if randomness cannot be read. The returned bytes are
// independent test storage used by Encrypt and Decrypt.
func makeKey(t *testing.T) []byte {
	t.Helper()
	// Allocate the exact AES-256 key size in bytes.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate random key: %v", err)
	}
	return key
}

// TestEncryptDecryptRoundTrip encrypts varied ASCII, JSON, Unicode and longer
// plaintext with one test key. Each decrypted result must equal its supplied
// bytes, protecting historical ciphertext round trips across these fixtures.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	plaintexts := []string{
		"hello world",
		"short",
		"a longer string with special chars: !@#$%^&*()",
		`{"key":"value","num":42}`,
		"你好世界 unicode test 🎉",
		strings.Repeat("x", 10000),
	}

	// Bound plaintext failure excerpts to thirty bytes for diagnostics.
	for _, pt := range plaintexts {
		encrypted, err := Encrypt(pt, key)
		if err != nil {
			t.Fatalf("Encrypt(%q) error: %v", pt[:min(len(pt), 30)], err)
		}
		decrypted, err := Decrypt(encrypted, key)
		if err != nil {
			t.Fatalf("Decrypt error for plaintext %q: %v", pt[:min(len(pt), 30)], err)
		}
		if decrypted != pt {
			t.Fatalf("round-trip failed: got %q, want %q", decrypted[:min(len(decrypted), 30)], pt[:min(len(pt), 30)])
		}
	}
}

// TestEncryptProducesDifferentCiphertexts encrypts the same plaintext twice
// with one key. The serialized results must differ, exercising the fresh nonce
// behavior for this pair of calls.
func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	ct1, err := Encrypt("same plaintext", key)
	if err != nil {
		t.Fatalf("first Encrypt error: %v", err)
	}
	ct2, err := Encrypt("same plaintext", key)
	if err != nil {
		t.Fatalf("second Encrypt error: %v", err)
	}
	if ct1 == ct2 {
		t.Fatal("two encryptions of the same plaintext should produce different ciphertexts (random nonce)")
	}
}

// TestDecryptWithWrongKeyFails decrypts one ciphertext using a separately
// generated key. Authentication must report an error, protecting the tested
// wrong-key path from returning accepted plaintext.
func TestDecryptWithWrongKeyFails(t *testing.T) {
	t.Parallel()
	key1 := makeKey(t)
	key2 := makeKey(t)

	encrypted, err := Encrypt("secret data", key1)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	_, err = Decrypt(encrypted, key2)
	if err == nil {
		t.Fatal("Decrypt with wrong key should fail, but got nil error")
	}
}

// TestEncryptRejectsInvalidKeyLength supplies nil, empty and several
// short/long key sizes. Every tested Encrypt call must return an error before
// producing a usable historical ciphertext value.
func TestEncryptRejectsInvalidKeyLength(t *testing.T) {
	t.Parallel()
	badKeys := [][]byte{
		nil,
		make([]byte, 0),
		make([]byte, 16),
		make([]byte, 31),
		make([]byte, 33),
		make([]byte, 64),
	}
	for _, key := range badKeys {
		_, err := Encrypt("test", key)
		if err == nil {
			t.Fatalf("Encrypt should reject key of length %d", len(key))
		}
	}
}

// TestDecryptRejectsInvalidKeyLength supplies nil, short and long keys with
// dummy ciphertext text. Each tested call must report a key-length error
// rather than attempting that ciphertext as a usable configuration.
func TestDecryptRejectsInvalidKeyLength(t *testing.T) {
	t.Parallel()
	badKeys := [][]byte{
		nil,
		make([]byte, 16),
		make([]byte, 33),
	}
	for _, key := range badKeys {
		_, err := Decrypt("dummydata:dummydata:dummydata", key)
		if err == nil {
			t.Fatalf("Decrypt should reject key of length %d", len(key))
		}
	}
}

// TestEncryptEmptyPlaintext encrypts an empty value with a valid test key.
// Decrypt must restore empty text successfully, protecting the distinction
// between valid empty plaintext and invalid ciphertext format.
func TestEncryptEmptyPlaintext(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	encrypted, err := Encrypt("", key)
	if err != nil {
		t.Fatalf("Encrypt empty plaintext error: %v", err)
	}
	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("Decrypt empty plaintext error: %v", err)
	}
	if decrypted != "" {
		t.Fatalf("expected empty string, got %q", decrypted)
	}
}

// TestEncryptDecryptUnicodeJSON uses JSON text containing Chinese characters
// and numeric content. The decrypted string must retain the complete supplied
// bytes, including its Unicode and literal number spelling.
func TestEncryptDecryptUnicodeJSON(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	jsonContent := `{"name":"测试用户","email":"test@example.com","balance":100.50}`
	encrypted, err := Encrypt(jsonContent, key)
	if err != nil {
		t.Fatalf("Encrypt JSON error: %v", err)
	}
	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("Decrypt JSON error: %v", err)
	}
	if decrypted != jsonContent {
		t.Fatalf("JSON round-trip failed: got %q, want %q", decrypted, jsonContent)
	}
}

// TestDecryptInvalidFormat supplies empty, missing-delimiter, two-field and
// bad-base64 values with a valid key. Each listed case must return an error;
// this fixture set does not exercise a validly decoded wrong-size nonce.
func TestDecryptInvalidFormat(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	invalidInputs := []string{
		"",
		"nodelimiter",
		"only:two",
		"invalid:base64:!!!",
	}
	for _, input := range invalidInputs {
		_, err := Decrypt(input, key)
		if err == nil {
			t.Fatalf("Decrypt(%q) should fail but got nil error", input)
		}
	}
}

// TestCiphertextFormat encrypts nonempty text and splits its serialized
// representation into three fields. It requires three nonblank parts,
// protecting nonce/tag/ciphertext transport structure for this input.
func TestCiphertextFormat(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	encrypted, err := Encrypt("test", key)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	parts := strings.SplitN(encrypted, ":", 3)
	if len(parts) != 3 {
		t.Fatalf("ciphertext should have format iv:authTag:ciphertext, got %d parts", len(parts))
	}
	for i, part := range parts {
		if part == "" {
			t.Fatalf("ciphertext part %d is empty", i)
		}
	}
}
