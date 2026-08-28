package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrProvider = errors.New("GitHub authentication failed; start login again")

type GitHub struct {
	clientID, clientSecret, callback string
	client                           *http.Client
}

func NewGitHub(clientID, clientSecret, callback string) (*GitHub, error) {
	u, err := url.Parse(callback)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "/api/v1/auth/github/callback" ||
		clientID == "" || len(clientSecret) < 16 || len(clientSecret) > 4096 || strings.ContainsAny(clientID+clientSecret, "\r\n\x00") {
		return nil, errors.New("invalid GitHub OAuth configuration")
	}
	return &GitHub{clientID: clientID, clientSecret: clientSecret, callback: callback,
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirect refused") }}}, nil
}

func (g *GitHub) AuthorizationURL(state, verifier string) string {
	query := url.Values{"client_id": {g.clientID}, "redirect_uri": {g.callback}, "state": {state},
		"code_challenge": {PKCEChallenge(verifier)}, "code_challenge_method": {"S256"}, "allow_signup": {"false"}, "prompt": {"select_account"}}
	return GitHubInstance + "/login/oauth/authorize?" + query.Encode()
}

func (g *GitHub) Exchange(ctx context.Context, code, verifier string) (ExternalUser, error) {
	if len(code) == 0 || len(code) > 1024 {
		return ExternalUser{}, ErrProvider
	}
	if _, err := TokenDigest(verifier); err != nil {
		return ExternalUser{}, ErrProvider
	}
	form := url.Values{"client_id": {g.clientID}, "client_secret": {g.clientSecret}, "redirect_uri": {g.callback}, "code": {code}, "code_verifier": {verifier}}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, GitHubInstance+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return ExternalUser{}, ErrProvider
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	var reply struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	if err := g.requestJSON(r, &reply); err != nil {
		return ExternalUser{}, ErrProvider
	}
	if reply.Error != "" || reply.AccessToken == "" || len(reply.AccessToken) > 4096 || strings.ContainsAny(reply.AccessToken, " \r\n\x00") || !strings.EqualFold(reply.TokenType, "bearer") {
		return ExternalUser{}, ErrProvider
	}
	r, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return ExternalUser{}, ErrProvider
	}
	r.Header.Set("Authorization", "Bearer "+reply.AccessToken)
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	var profile struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := g.requestJSON(r, &profile); err != nil {
		return ExternalUser{}, ErrProvider
	}
	user := ExternalUser{Provider: "github", Instance: GitHubInstance, Subject: strconv.FormatInt(profile.ID, 10), Login: profile.Login, Name: profile.Name}
	if !user.Valid() {
		return ExternalUser{}, ErrProvider
	}
	return user, nil
}

func (g *GitHub) requestJSON(request *http.Request, target any) error {
	response, err := g.client.Do(request)
	if err != nil {
		return ErrProvider
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrProvider
	}
	const maxReply = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReply+1))
	if err != nil || len(body) > maxReply || json.Unmarshal(body, target) != nil {
		return ErrProvider
	}
	return nil
}
