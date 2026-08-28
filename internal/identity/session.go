// Package identity defines browser session credentials and persistence ports.
// Only a verified authentication flow may issue sessions for an existing user.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

var ErrUnauthenticated = errors.New("session is invalid or expired")

const CookieName = "__Host-yuanci_session"
const DefaultTTL = 8 * time.Hour

type Session struct {
	ID          uuid.UUID `json:"-"`
	UserID      uuid.UUID `json:"user_id"`
	DisplayName string    `json:"display_name"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Credentials must never be serialized or logged. Token is returned once to
// the trusted OAuth callback, which writes it only as an HttpOnly cookie.
type Credentials struct {
	Session Session `json:"-"`
	Token   string  `json:"-"`
}

type Sessions interface {
	AuthenticateSession(context.Context, string) (Session, error)
	RevokeSession(context.Context, string) error
}

func NewToken() string {
	var value [32]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func TokenDigest(token string) ([32]byte, error) {
	if len(token) != 43 {
		return [32]byte{}, ErrUnauthenticated
	}
	value, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(value) != 32 {
		return [32]byte{}, ErrUnauthenticated
	}
	return sha256.Sum256([]byte(token)), nil
}

func CSRFToken(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("yuanci:browser-csrf:v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func ValidCSRF(token, provided string) bool {
	if _, err := TokenDigest(token); err != nil || len(provided) != 43 {
		return false
	}
	return hmac.Equal([]byte(CSRFToken(token)), []byte(provided))
}

func SessionCookie(credentials Credentials) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: credentials.Token, Path: "/",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: credentials.Session.ExpiresAt}
}

func ExpiredCookie() *http.Cookie {
	cookie := SessionCookie(Credentials{Session: Session{ExpiresAt: time.Unix(1, 0)}})
	cookie.MaxAge = -1
	return cookie
}
