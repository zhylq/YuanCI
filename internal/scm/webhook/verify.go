package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrMissingSignature = errors.New("webhook signature is missing")
	ErrInvalidSignature = errors.New("webhook signature is invalid")
	ErrStaleDelivery    = errors.New("webhook timestamp is outside the accepted window")
)

// VerifyHMACSHA256 validates the raw-body HMAC used by GitHub and Gitea.
// GitHub sends a sha256= prefix; Gitea's native X-Gitea-Signature has no prefix.
func VerifyHMACSHA256(secret, body []byte, received, prefix string) error {
	if len(secret) < 16 {
		return errors.New("webhook secret must contain at least 16 bytes")
	}
	if received == "" {
		return ErrMissingSignature
	}
	if prefix != "" {
		if !strings.HasPrefix(received, prefix) {
			return ErrInvalidSignature
		}
		received = strings.TrimPrefix(received, prefix)
	}
	provided, err := hex.DecodeString(received)
	if err != nil || len(provided) != sha256.Size {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), provided) {
		return ErrInvalidSignature
	}
	return nil
}

// VerifyStandard validates Standard Webhooks signatures used by GitLab 19.1+.
// signingToken must be the complete whsec_<base64> value returned at creation.
func VerifyStandard(signingToken, messageID, timestamp, signatures string, body []byte, now time.Time, maxSkew time.Duration) error {
	if messageID == "" || timestamp == "" || signatures == "" {
		return ErrMissingSignature
	}
	if !strings.HasPrefix(signingToken, "whsec_") {
		return errors.New("signing token must start with whsec_")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(signingToken, "whsec_"))
	if err != nil || len(key) != 32 {
		return errors.New("signing token must encode exactly 32 bytes")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	issuedAt := time.Unix(seconds, 0)
	if now.Sub(issuedAt) > maxSkew || issuedAt.Sub(now) > maxSkew {
		return ErrStaleDelivery
	}

	message := append([]byte(messageID+"."+timestamp+"."), body...)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	expected := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, signature := range strings.Fields(signatures) {
		if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1 {
			return nil
		}
	}
	return ErrInvalidSignature
}

// VerifyLegacyToken supports older GitLab installations during migration to
// signing tokens. New webhooks must use VerifyStandard instead.
func VerifyLegacyToken(expected, received string) error {
	if expected == "" || received == "" {
		return ErrMissingSignature
	}
	if len(expected) != len(received) || subtle.ConstantTimeCompare([]byte(expected), []byte(received)) != 1 {
		return ErrInvalidSignature
	}
	return nil
}

func PayloadDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return fmt.Sprintf("%x", digest[:])
}
