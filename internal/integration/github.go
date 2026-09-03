package integration

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/yuanci/yuanci/internal/identity"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$`)
var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type GitHub struct{ client *http.Client }

func NewGitHub() *GitHub {
	return &GitHub{client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}
}
func parseKey(data []byte) (*rsa.PrivateKey, error) {
	if len(data) > 16<<10 {
		return nil, ErrConfig
	}
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || len(block.Headers) != 0 {
		return nil, ErrConfig
	}
	var key *rsa.PrivateKey
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var raw any
		raw, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = raw.(*rsa.PrivateKey)
	default:
		return nil, ErrConfig
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 || key.N.BitLen() > 4096 || key.Validate() != nil {
		return nil, ErrConfig
	}
	return key, nil
}
func appJWT(clientID string, data []byte) (string, error) {
	key, err := parseKey(data)
	if err != nil {
		return "", err
	}
	claims, _ := json.Marshal(map[string]any{"iss": clientID, "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()})
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", ErrConfig
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
func (g *GitHub) request(ctx context.Context, method, path, token string, body io.Reader, out any) error {
	endpoint := "https://api.github.com" + path
	if path == "/login/oauth/access_token" {
		endpoint = "https://github.com" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return ErrRemote
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if token != "" {
		if len(token) > 8192 || strings.ContainsAny(token, " \r\n\x00") {
			return ErrRemote
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := g.client.Do(req)
	if err != nil {
		return ErrRemote
	}
	defer res.Body.Close()
	if res.StatusCode == 429 || (res.StatusCode == 403 && (res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != "")) {
		return ErrRate
	}
	if res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404 {
		return ErrAccess
	}
	if res.StatusCode != 200 {
		return ErrRemote
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || json.Unmarshal(data, out) != nil {
		return ErrRemote
	}
	return nil
}
func (g *GitHub) VerifyApp(ctx context.Context, clientID string, key []byte) (App, error) {
	jwt, err := appJWT(clientID, key)
	if err != nil {
		return App{}, err
	}
	var raw struct {
		ID       int64  `json:"id"`
		Slug     string `json:"slug"`
		ClientID string `json:"client_id"`
	}
	if err := g.request(ctx, "GET", "/app", jwt, nil, &raw); err != nil {
		return App{}, err
	}
	if raw.ID <= 0 || raw.ClientID != clientID || !slugPattern.MatchString(raw.Slug) {
		return App{}, ErrConfig
	}
	return App{AppID: strconv.FormatInt(raw.ID, 10), ClientID: raw.ClientID, Slug: raw.Slug}, nil
}
func (g *GitHub) AuthorizationURL(clientID, callback, state, verifier string) string {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {callback}, "state": {state}, "code_challenge": {identity.PKCEChallenge(verifier)}, "code_challenge_method": {"S256"}, "allow_signup": {"false"}, "prompt": {"select_account"}}
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}
func (g *GitHub) Exchange(ctx context.Context, clientID, secret, callback, code, verifier string) (string, string, time.Time, error) {
	fail := func(err error) (string, string, time.Time, error) { return "", "", time.Time{}, err }
	if code == "" || len(code) > 1024 {
		return fail(ErrRemote)
	}
	if _, err := identity.TokenDigest(verifier); err != nil {
		return fail(ErrRemote)
	}
	q := url.Values{"client_id": {clientID}, "client_secret": {secret}, "redirect_uri": {callback}, "code": {code}, "code_verifier": {verifier}}
	var reply struct {
		Token   string `json:"access_token"`
		Type    string `json:"token_type"`
		Error   string `json:"error"`
		Expires int64  `json:"expires_in"`
	}
	if err := g.request(ctx, "POST", "/login/oauth/access_token", "", strings.NewReader(q.Encode()), &reply); err != nil {
		return fail(err)
	}
	if reply.Expires < 0 || reply.Error != "" || reply.Token == "" || !strings.EqualFold(reply.Type, "bearer") || len(reply.Token) > 8192 || strings.ContainsAny(reply.Token, " \r\n\x00") {
		return fail(ErrRemote)
	}
	ttl := 10 * time.Minute
	if reply.Expires > 0 && reply.Expires < 600 {
		ttl = time.Duration(reply.Expires) * time.Second
	}
	expiry := time.Now().Add(ttl)
	var user struct {
		ID int64 `json:"id"`
	}
	if err := g.request(ctx, "GET", "/user", reply.Token, nil, &user); err != nil {
		return fail(err)
	}
	if user.ID <= 0 {
		return fail(ErrAccess)
	}
	return strconv.FormatInt(user.ID, 10), reply.Token, expiry, nil
}

type installationReply struct {
	ID        int64   `json:"id"`
	AppID     int64   `json:"app_id"`
	Suspended *string `json:"suspended_at"`
	Account   struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"account"`
}

func (g *GitHub) Installations(ctx context.Context, token string) ([]Installation, error) {
	items := []Installation{}
	for page := 1; page <= 10; page++ {
		var reply struct {
			Items []installationReply `json:"installations"`
		}
		if err := g.request(ctx, "GET", "/user/installations?per_page=100&page="+strconv.Itoa(page), token, nil, &reply); err != nil {
			return nil, err
		}
		if len(reply.Items) > 100 {
			return nil, ErrRemote
		}
		for _, raw := range reply.Items {
			if raw.ID > 0 && raw.Account.ID > 0 && raw.Suspended == nil && slugPattern.MatchString(raw.Account.Login) {
				items = append(items, Installation{ID: strconv.FormatInt(raw.ID, 10), AccountID: strconv.FormatInt(raw.Account.ID, 10), Account: raw.Account.Login})
			}
		}
		if len(reply.Items) < 100 {
			return items, nil
		}
	}
	return nil, ErrRemote
}
func (g *GitHub) VerifyInstallation(ctx context.Context, app App, key []byte, install Installation) error {
	if !identity.ValidGitHubSubject(install.ID) {
		return ErrAccess
	}
	jwt, err := appJWT(app.ClientID, key)
	if err != nil {
		return err
	}
	var raw installationReply
	if err := g.request(ctx, "GET", "/app/installations/"+install.ID, jwt, nil, &raw); err != nil {
		return err
	}
	if strconv.FormatInt(raw.ID, 10) != install.ID || strconv.FormatInt(raw.AppID, 10) != app.AppID || raw.Suspended != nil || strconv.FormatInt(raw.Account.ID, 10) != install.AccountID || raw.Account.Login != install.Account {
		return ErrAccess
	}
	return nil
}

func (g *GitHub) InstallationToken(ctx context.Context, clientID string, key []byte, installID, repositoryID string) ([]byte, time.Time, error) {
	failed := func(err error) ([]byte, time.Time, error) { return nil, time.Time{}, err }
	if !identity.ValidGitHubSubject(installID) || !identity.ValidGitHubSubject(repositoryID) {
		return failed(ErrConfig)
	}
	repositoryNumber, err := strconv.ParseInt(repositoryID, 10, 64)
	if err != nil || repositoryNumber <= 0 {
		return failed(ErrConfig)
	}
	jwt, err := appJWT(clientID, key)
	if err != nil {
		return failed(err)
	}
	body, err := json.Marshal(map[string]any{
		"repository_ids": []int64{repositoryNumber},
		"permissions":    map[string]string{"contents": "read"},
	})
	if err != nil {
		return failed(ErrRemote)
	}
	endpoint := "https://api.github.com/app/installations/" + installID + "/access_tokens"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return failed(ErrRemote)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "YuanCI/0.1")
	response, err := g.client.Do(request)
	if err != nil {
		return failed(ErrRemote)
	}
	defer response.Body.Close()
	if err := runtimeResponseError(response, http.StatusCreated); err != nil {
		return failed(err)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return failed(ErrRemote)
	}
	var reply struct {
		Token        string    `json:"token"`
		ExpiresAt    time.Time `json:"expires_at"`
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
		Permissions map[string]string `json:"permissions"`
	}
	if json.Unmarshal(data, &reply) != nil || len(reply.Token) < 1 || len(reply.Token) > 8192 ||
		strings.ContainsAny(reply.Token, " \r\n\x00") || len(reply.Repositories) != 1 ||
		reply.Repositories[0].ID != repositoryNumber || reply.Permissions["contents"] != "read" {
		return failed(ErrRemote)
	}
	for name, permission := range reply.Permissions {
		if permission != "read" || (name != "contents" && name != "metadata") {
			return failed(ErrAccess)
		}
	}
	return []byte(reply.Token), reply.ExpiresAt, nil
}

func (g *GitHub) RepositoryFile(ctx context.Context, token []byte, owner, name, filePath, sha string) ([]byte, error) {
	if len(token) < 1 || len(token) > 8192 || bytes.IndexFunc(token, func(r rune) bool { return r == ' ' || r == '\r' || r == '\n' || r == 0 }) >= 0 ||
		!repoPattern.MatchString(owner) || !repoPattern.MatchString(name) || owner == "." || owner == ".." || name == "." || name == ".." || !commitPattern.MatchString(sha) {
		return nil, ErrConfig
	}
	escapedPath, err := runtimeRepositoryPath(filePath)
	if err != nil {
		return nil, err
	}
	endpoint, err := url.Parse("https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/contents/" + escapedPath)
	if err != nil {
		return nil, ErrConfig
	}
	query := endpoint.Query()
	query.Set("ref", sha)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, ErrRemote
	}
	request.Header.Set("Accept", "application/vnd.github.raw+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("User-Agent", "YuanCI/0.1")
	response, err := g.client.Do(request)
	if err != nil {
		return nil, ErrRemote
	}
	defer response.Body.Close()
	if err := runtimeResponseError(response, http.StatusOK); err != nil {
		return nil, err
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(content) > 1<<20 {
		return nil, ErrRemote
	}
	return content, nil
}

func (g *GitHub) RepositoryCommit(ctx context.Context, token []byte, owner, name, ref string) (string, error) {
	if len(token) < 1 || len(token) > 8192 || bytes.IndexFunc(token, func(r rune) bool { return r == ' ' || r == '\r' || r == '\n' || r == 0 }) >= 0 ||
		!repoPattern.MatchString(owner) || !repoPattern.MatchString(name) || owner == "." || owner == ".." || name == "." || name == ".." ||
		len(ref) < 1 || len(ref) > 255 || strings.HasPrefix(ref, "/") || strings.ContainsAny(ref, "\r\n\x00") {
		return "", ErrConfig
	}
	if _, err := runtimeRepositoryPath(ref); err != nil {
		return "", err
	}
	endpoint, err := url.Parse("https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/commits/" + url.PathEscape(ref))
	if err != nil {
		return "", ErrConfig
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", ErrRemote
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("User-Agent", "YuanCI/0.1")
	response, err := g.client.Do(request)
	if err != nil {
		return "", ErrRemote
	}
	defer response.Body.Close()
	if err := runtimeResponseError(response, http.StatusOK); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return "", ErrRemote
	}
	var reply struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(data, &reply) != nil || !commitPattern.MatchString(reply.SHA) {
		return "", ErrRemote
	}
	return strings.ToLower(reply.SHA), nil
}

func runtimeRepositoryPath(value string) (string, error) {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", ErrConfig
	}
	parts := strings.Split(value, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\r\n\x00") {
			return "", ErrConfig
		}
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), nil
}

func runtimeResponseError(response *http.Response, expected int) error {
	if response.StatusCode == expected {
		return nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && (response.Header.Get("X-RateLimit-Remaining") == "0" || response.Header.Get("Retry-After") != "")) {
		return ErrRate
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
		return ErrAccess
	}
	return ErrRemote
}

func (g *GitHub) Repositories(ctx context.Context, token, installID string, page int) (RepoPage, error) {
	if !identity.ValidGitHubSubject(installID) || page < 1 || page > 100 {
		return RepoPage{}, ErrConfig
	}
	var reply struct {
		Total int `json:"total_count"`
		Items []struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			DefaultBranch string `json:"default_branch"`
			Owner         struct {
				Login string `json:"login"`
			} `json:"owner"`
			Permissions struct {
				Admin bool `json:"admin"`
			} `json:"permissions"`
		} `json:"repositories"`
	}
	if err := g.request(ctx, "GET", "/user/installations/"+installID+"/repositories?per_page=100&page="+strconv.Itoa(page), token, nil, &reply); err != nil {
		return RepoPage{}, err
	}
	if reply.Total < 0 || len(reply.Items) > 100 {
		return RepoPage{}, ErrRemote
	}
	result := RepoPage{Items: []Repository{}}
	for _, raw := range reply.Items {
		if raw.Permissions.Admin && raw.ID > 0 && slugPattern.MatchString(raw.Owner.Login) && repoPattern.MatchString(raw.Name) && raw.Name != "." && raw.Name != ".." && len(raw.DefaultBranch) <= 255 && !strings.ContainsAny(raw.DefaultBranch, "\r\n\x00") {
			result.Items = append(result.Items, Repository{ID: strconv.FormatInt(raw.ID, 10), Owner: raw.Owner.Login, Name: raw.Name, DefaultBranch: raw.DefaultBranch})
		}
	}
	if page*100 < reply.Total {
		if page == 100 {
			return RepoPage{}, ErrRemote
		}
		result.NextPage = page + 1
	}
	return result, nil
}
