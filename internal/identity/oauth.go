package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	GitHubInstance = "https://github.com"
	GiteeInstance  = "https://gitee.com"
)
const FlowCookieName = "__Host-yuanci_oauth"
const FlowTTL = 5 * time.Minute

var ErrOAuthFlow = errors.New("login flow is invalid or expired; start again")
var ErrIdentityConflict = errors.New("external identity belongs to another account")
var ErrBootstrap = errors.New("administrator bootstrap configuration conflicts with persisted state")
var ErrFlowCapacity = errors.New("too many pending login flows")

type ExternalUser struct {
	Provider string
	Instance string
	Subject  string
	Login    string
	Name     string
}

func ValidGitHubSubject(subject string) bool {
	id, err := strconv.ParseInt(subject, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == subject
}

// ValidProviderInstance restricts currently enabled public providers to their
// canonical HTTPS origins. Self-hosted Gitea remains unavailable until GT-01.
func ValidProviderInstance(provider, instance string) bool {
	switch provider {
	case "github":
		return instance == GitHubInstance
	case "gitee":
		return instance == GiteeInstance
	default:
		return false
	}
}

func (user ExternalUser) Valid() bool {
	return ValidProviderInstance(user.Provider, user.Instance) && ValidGitHubSubject(user.Subject) &&
		len(user.Login) > 0 && len(user.Login) <= 100 && len(user.Name) <= 256 && !strings.ContainsAny(user.Login+user.Name, "\r\n\x00")
}

type OAuthStore interface {
	BeginOAuth(context.Context, string, string, string) error
	ConsumeOAuth(context.Context, string, string) (string, error)
	FinishOAuth(context.Context, string, ExternalUser, string) (Credentials, error)
}

type OAuthProvider interface {
	AuthorizationURL(state, verifier string) string
	Exchange(context.Context, string, string) (ExternalUser, error)
}

func PKCEVerifier(state, nonce string) string {
	mac := hmac.New(sha256.New, []byte(nonce))
	_, _ = mac.Write([]byte("yuanci:oauth:pkce:v1:" + state))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func PKCEChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func FlowCookie(nonce string) *http.Cookie {
	return &http.Cookie{Name: FlowCookieName, Value: nonce, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(FlowTTL.Seconds())}
}
