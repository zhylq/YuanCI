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
