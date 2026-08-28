package identity

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTokenAndCSRF(t *testing.T) {
	token := NewToken()
	if token == NewToken() {
		t.Fatal("duplicate token")
	}
	digest, err := TokenDigest(token)
	if err != nil || digest == [32]byte{} {
		t.Fatal("invalid generated token")
	}
	for _, bad := range []string{"", "bad", strings.Repeat("!", 43), token + "=", strings.Repeat("a", 42) + "b"} {
		if _, err := TokenDigest(bad); err == nil {
			t.Fatal("malformed token accepted")
		}
	}
	if !ValidCSRF(token, CSRFToken(token)) || ValidCSRF(NewToken(), CSRFToken(token)) || ValidCSRF(token, "") {
		t.Fatal("CSRF binding broken")
	}
	if ValidCSRF("", CSRFToken("")) {
		t.Fatal("invalid session CSRF accepted")
	}
}

func TestCookieAndCredentialSerialization(t *testing.T) {
	credentials := Credentials{Token: NewToken(), Session: Session{ExpiresAt: time.Now().Add(time.Hour)}}
	cookie := SessionCookie(credentials)
	if cookie.Name != CookieName || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal("unsafe cookie flags")
	}
	encoded, err := json.Marshal(credentials)
	if err != nil || strings.Contains(string(encoded), credentials.Token) {
		t.Fatal("credential serialization leaked token")
	}
	expired := ExpiredCookie()
	if expired.Value != "" || expired.MaxAge != -1 || !expired.Secure {
		t.Fatal("invalid logout cookie")
	}
}
