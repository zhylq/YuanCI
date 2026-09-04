package identity

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

	"github.com/yuanci/yuanci/internal/scm"
)

// Gitee uses its documented confidential authorization-code flow. PKCE is not
// advertised by Gitee; callers must consume the browser-bound state before
// Exchange. Login requests only user_info and does not retain access tokens.
type Gitee struct {
	clientID, clientSecret, callback string
	client                           *http.Client
}

func NewGitee(id, secret, callback string) (*Gitee, error) {
	u, err := url.Parse(callback)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Path != "/api/v1/auth/gitee/callback" || id == "" || len(id) > 4096 || len(secret) < 16 || len(secret) > 4096 || strings.ContainsAny(id+secret, "\r\n\x00") {
		return nil, errors.New("invalid Gitee OAuth configuration")
	}
	return &Gitee{clientID: id, clientSecret: secret, callback: callback, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirect refused") }}}, nil
}

func (g *Gitee) AuthorizationURL(state, verifier string) string {
	return GiteeInstance + "/oauth/authorize?" + url.Values{"client_id": {g.clientID}, "redirect_uri": {g.callback}, "response_type": {"code"}, "scope": {"user_info"}, "state": {state}}.Encode()
}

func (g *Gitee) Exchange(ctx context.Context, code, verifier string) (ExternalUser, error) {
	if len(code) == 0 || len(code) > 1024 || strings.ContainsAny(code, "\r\n\x00") {
		return ExternalUser{}, ErrProvider
	}
	if _, err := TokenDigest(verifier); err != nil {
		return ExternalUser{}, ErrProvider
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {g.clientID}, "client_secret": {g.clientSecret}, "redirect_uri": {g.callback}, "code": {code}}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, GiteeInstance+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return ExternalUser{}, ErrProvider
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		Access  string `json:"access_token"`
		Type    string `json:"token_type"`
		Scope   string `json:"scope"`
		Expires int64  `json:"expires_in"`
		Error   string `json:"error"`
	}
	if err := g.requestJSON(r, &token); err != nil {
		return ExternalUser{}, err
	}
	if token.Error != "" || token.Access == "" || len(token.Access) > 4096 || strings.ContainsAny(token.Access, " \r\n\x00") || !strings.EqualFold(token.Type, "bearer") || token.Expires <= 0 || token.Expires > 366*86400 {
		return ExternalUser{}, ErrProvider
	}
	if !slices.Contains(strings.Fields(token.Scope), "user_info") {
		return ExternalUser{}, errors.Join(ErrProvider, scm.ErrUnauthorized)
	}
	r, err = http.NewRequestWithContext(ctx, http.MethodGet, GiteeInstance+"/api/v5/user", nil)
	if err != nil {
		return ExternalUser{}, ErrProvider
	}
	r.Header.Set("Authorization", "Bearer "+token.Access)
	var profile struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := g.requestJSON(r, &profile); err != nil {
		return ExternalUser{}, err
	}
	user := ExternalUser{Provider: "gitee", Instance: GiteeInstance, Subject: strconv.FormatInt(profile.ID, 10), Login: profile.Login, Name: profile.Name}
	if !user.Valid() {
		return ExternalUser{}, ErrProvider
	}
	return user, nil
}

func (g *Gitee) requestJSON(r *http.Request, target any) error {
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", "YuanCI")
	response, err := g.client.Do(r)
	if err != nil {
		return ErrProvider
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.Join(ErrProvider, scm.ErrUnauthorized)
	case http.StatusTooManyRequests:
		return errors.Join(ErrProvider, scm.ErrRateLimited)
	case http.StatusOK:
	default:
		return ErrProvider
	}
	const maxReply = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReply+1))
	defer clear(body)
	if err != nil || len(body) > maxReply || json.Unmarshal(body, target) != nil {
		return ErrProvider
	}
	return nil
}
