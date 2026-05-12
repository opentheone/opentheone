package crypto

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	cases := []string{
		"sk-1234567890",
		"a",
		"包含中文 with 🚀 emoji",
		strings.Repeat("x", 4096),
	}
	for _, pt := range cases {
		ct, err := Encrypt("test-secret", pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		if ct == pt {
			t.Errorf("ciphertext must not equal plaintext for %q", pt)
		}
		got, err := Decrypt("test-secret", ct)
		if err != nil {
			t.Fatalf("Decrypt(%q): %v", pt, err)
		}
		if got != pt {
			t.Errorf("round trip mismatch: got %q, want %q", got, pt)
		}
	}
}

func TestEncryptEmptyReturnsEmpty(t *testing.T) {
	ct, err := Encrypt("any", "")
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	if ct != "" {
		t.Errorf("Encrypt(\"\") should be \"\", got %q", ct)
	}
	got, err := Decrypt("any", "")
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if got != "" {
		t.Errorf("Decrypt(\"\") should be \"\", got %q", got)
	}
}

func TestDecryptWithWrongSecretFails(t *testing.T) {
	ct, err := Encrypt("right-secret", "secret-payload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt("wrong-secret", ct); err == nil {
		t.Fatal("Decrypt with wrong secret should have failed")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	ct, err := Encrypt("k", "hello")
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte near the end of the base64 payload.
	tampered := ct[:len(ct)-2] + "AA"
	if tampered == ct {
		t.Skip("tamper produced identical ciphertext, retry not worth it")
	}
	if _, err := Decrypt("k", tampered); err == nil {
		t.Fatal("Decrypt of tampered ciphertext should have failed")
	}
}

func TestDecryptShortCiphertextFails(t *testing.T) {
	if _, err := Decrypt("k", "AA=="); err == nil {
		t.Fatal("Decrypt of too-short ciphertext should have failed")
	}
}

func TestEncryptIsNondeterministic(t *testing.T) {
	a, _ := Encrypt("k", "same plaintext")
	b, _ := Encrypt("k", "same plaintext")
	if a == b {
		t.Fatal("Two encryptions of the same plaintext should differ (random nonce)")
	}
}
