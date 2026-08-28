package secrets

import (
	"bytes"
	"testing"
)

func TestEnvelopeRoundTripAndAADBinding(t *testing.T) {
	master := bytes.Repeat([]byte{7}, 32)
	cipher, err := NewCipher(master)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := cipher.Seal([]byte("registry-password"), []byte("project:one/secret:registry"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Open(envelope, []byte("project:one/secret:registry"))
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "registry-password" {
		t.Fatalf("unexpected plaintext %q", plaintext)
	}
	if _, err := cipher.Open(envelope, []byte("project:two/secret:registry")); err == nil {
		t.Fatal("expected AAD mismatch to fail")
	}
}

func TestGenerateMasterKeyIsParseable(t *testing.T) {
	encoded, err := GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseMasterKey(encoded)
	if err != nil || len(value) != 32 {
		t.Fatalf("ParseMasterKey() len=%d err=%v", len(value), err)
	}
}

func TestMalformedEnvelopeReturnsError(t *testing.T) {
	cipher, err := NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Envelope){
		"missing key nonce":    func(e *Envelope) { e.KeyNonce = nil },
		"short key nonce":      func(e *Envelope) { e.KeyNonce = e.KeyNonce[:1] },
		"long key nonce":       func(e *Envelope) { e.KeyNonce = append(e.KeyNonce, 0) },
		"missing data nonce":   func(e *Envelope) { e.DataNonce = nil },
		"short data nonce":     func(e *Envelope) { e.DataNonce = e.DataNonce[:1] },
		"long data nonce":      func(e *Envelope) { e.DataNonce = append(e.DataNonce, 0) },
		"empty wrapped key":    func(e *Envelope) { e.EncryptedDataKey = nil },
		"tampered wrapped key": func(e *Envelope) { e.EncryptedDataKey[0] ^= 1 },
		"empty ciphertext":     func(e *Envelope) { e.Ciphertext = nil },
		"tampered ciphertext":  func(e *Envelope) { e.Ciphertext[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			e, err := cipher.Seal([]byte("test-sensitive-value"), []byte("scope"))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&e)
			plaintext, err := cipher.Open(e, []byte("scope"))
			if err == nil || plaintext != nil {
				t.Fatal("malformed envelope returned plaintext or no error")
			}
			if bytes.Contains([]byte(err.Error()), []byte("test-sensitive-value")) {
				t.Fatal("error leaked plaintext")
			}
		})
	}
}

func FuzzOpenEnvelope(f *testing.F) {
	cipher, err := NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		f.Fatal(err)
	}
	e, err := cipher.Seal([]byte("fuzz-fixture"), []byte("scope"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(e.KeyNonce, e.DataNonce, e.EncryptedDataKey, e.Ciphertext, []byte("scope"))
	f.Add([]byte{}, e.DataNonce, e.EncryptedDataKey, e.Ciphertext, []byte("scope"))
	f.Add(e.KeyNonce, []byte{}, e.EncryptedDataKey, e.Ciphertext, []byte("scope"))
	f.Fuzz(func(t *testing.T, keyNonce, dataNonce, wrappedKey, ciphertext, aad []byte) {
		plaintext, err := cipher.Open(Envelope{KeyNonce: keyNonce, DataNonce: dataNonce,
			EncryptedDataKey: wrappedKey, Ciphertext: ciphertext}, aad)
		if err != nil && plaintext != nil {
			t.Fatal("failed authentication returned plaintext")
		}
	})
}
