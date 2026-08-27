package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func TestVerifyGitHubAndGiteaHMAC(t *testing.T) {
	secret := []byte("a-sufficiently-long-secret")
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	digest := hex.EncodeToString(mac.Sum(nil))
	if err := VerifyHMACSHA256(secret, body, "sha256="+digest, "sha256="); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHMACSHA256(secret, body, digest, ""); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHMACSHA256(secret, []byte("tampered"), digest, ""); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
}

func TestVerifyStandardWebhookAndReplayWindow(t *testing.T) {
	now := time.Unix(1_787_689_600, 0)
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	token := "whsec_" + base64.StdEncoding.EncodeToString(key)
	messageID, timestamp := "delivery-123", "1787689600"
	body := []byte(`{"object_kind":"push"}`)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(append([]byte(messageID+"."+timestamp+"."), body...))
	signature := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if err := VerifyStandard(token, messageID, timestamp, signature, body, now, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := VerifyStandard(token, messageID, timestamp, signature, body, now.Add(10*time.Minute), 5*time.Minute); !errors.Is(err, ErrStaleDelivery) {
		t.Fatalf("expected stale delivery, got %v", err)
	}
}

func TestLegacyTokenUsesConstantLengthComparison(t *testing.T) {
	if err := VerifyLegacyToken("expected", "expected"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLegacyToken("expected", "wrong"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
}
