// Package gitee implements Gitee.com repository authorization separately from login.
package gitee

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/scm"
)

var ErrRemote = errors.New("Gitee request failed")
var ErrStale = errors.New("Gitee authorization changed or is unavailable; authorize again")
var ErrBusy = errors.New("Gitee token renewal is in progress")

type RateError struct{ After time.Duration }

func (e RateError) Error() string { return "Gitee request rate limited" }
func (e RateError) Unwrap() error { return scm.ErrRateLimited }

type OAuthConfig struct{ ClientID, Secret, Callback string }
type Token struct {
	Access    string    `json:"access_token"`
	Refresh   string    `json:"refresh_token"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at"`
}
type OAuthProvider interface {
	Exchange(context.Context, OAuthConfig, string) (Token, error)
	Refresh(context.Context, OAuthConfig, string) (Token, error)
	User(context.Context, string) (identity.ExternalUser, error)
}
type Client struct{ http *http.Client }

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRemote }}}
}
func AuthorizationURL(config OAuthConfig, state string) string {
	return identity.GiteeInstance + "/oauth/authorize?" + url.Values{"client_id": {config.ClientID}, "redirect_uri": {config.Callback}, "response_type": {"code"}, "scope": {"user_info projects"}, "state": {state}}.Encode()
}
func (c *Client) Exchange(ctx context.Context, config OAuthConfig, code string) (Token, error) {
	if code == "" || len(code) > 1024 {
		return Token{}, ErrRemote
	}
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {config.ClientID}, "client_secret": {config.Secret}, "redirect_uri": {config.Callback}, "code": {code}})
}
func (c *Client) Refresh(ctx context.Context, config OAuthConfig, refresh string) (Token, error) {
	if !validToken(refresh) {
		return Token{}, scm.ErrUnauthorized
	}
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {config.ClientID}, "client_secret": {config.Secret}})
}
func validToken(value string) bool {
	return len(value) > 0 && len(value) <= 4096 && !strings.ContainsAny(value, " \r\n\x00")
}
func ValidScope(scope string) bool {
	fields := strings.Fields(scope)
	return slices.Contains(fields, "user_info") && slices.Contains(fields, "projects")
}
func (c *Client) token(ctx context.Context, form url.Values) (Token, error) {
	var reply struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Scope   string `json:"scope"`
		Expires int64  `json:"expires_in"`
		Created int64  `json:"created_at"`
		Error   string `json:"error"`
	}
	r, err := http.NewRequestWithContext(ctx, "POST", identity.GiteeInstance+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, ErrRemote
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := c.request(r, &reply, 1<<20); err != nil {
		return Token{}, err
	}
	if reply.Error != "" || !validToken(reply.Access) || !validToken(reply.Refresh) || !strings.EqualFold(reply.Type, "bearer") || reply.Expires <= 0 || reply.Expires > 366*86400 {
		return Token{}, ErrRemote
	}
	if !ValidScope(reply.Scope) {
		return Token{}, scm.ErrUnauthorized
	}
	now := time.Now()
	issued := now
	if reply.Created != 0 {
		issued = time.Unix(reply.Created, 0)
		if issued.After(now.Add(time.Minute)) {
			return Token{}, ErrRemote
		}
	}
	expiry := issued.Add(time.Duration(reply.Expires) * time.Second)
	if !expiry.After(now) {
		return Token{}, scm.ErrUnauthorized
	}
	return Token{Access: reply.Access, Refresh: reply.Refresh, Scope: reply.Scope, ExpiresAt: expiry}, nil
}
func (c *Client) User(ctx context.Context, token string) (identity.ExternalUser, error) {
	var reply struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := c.get(ctx, "/user", token, &reply, 1<<20); err != nil {
		return identity.ExternalUser{}, err
	}
	user := identity.ExternalUser{Provider: "gitee", Instance: identity.GiteeInstance, Subject: strconv.FormatInt(reply.ID, 10), Login: reply.Login, Name: reply.Name}
	if !user.Valid() {
		return identity.ExternalUser{}, ErrRemote
	}
	return user, nil
}
func (c *Client) get(ctx context.Context, path, token string, target any, limit int64) error {
	if !validToken(token) {
		return scm.ErrUnauthorized
	}
	r, err := http.NewRequestWithContext(ctx, "GET", identity.GiteeInstance+"/api/v5"+path, nil)
	if err != nil {
		return ErrRemote
	}
	values := r.URL.Query()
	values.Set("access_token", token)
	r.URL.RawQuery = values.Encode()
	return c.request(r, target, limit)
}
func (c *Client) request(r *http.Request, target any, limit int64) error {
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", "YuanCI")
	response, err := c.http.Do(r)
	if err != nil {
		return ErrRemote
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 401, 403:
		return scm.ErrUnauthorized
	case 404:
		return scm.ErrNotFound
	case 429:
		seconds, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		if seconds < 1 || seconds > 3600 {
			seconds = 60
		}
		return RateError{After: time.Duration(seconds) * time.Second}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrRemote
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	defer clear(body)
	if err != nil || int64(len(body)) > limit {
		return ErrRemote
	}
	if target != nil && json.Unmarshal(body, target) != nil {
		return ErrRemote
	}
	return nil
}
