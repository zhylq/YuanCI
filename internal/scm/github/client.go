package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/scm/webhook"
)

const (
	defaultBaseURL = "https://api.github.com"
	apiVersion     = "2026-03-10"
	maxResponse    = 10 << 20
	maxPipeline    = 1 << 20
)

type Client struct {
	httpClient    *http.Client
	baseURL       string
	token         string
	webhookSecret []byte
	now           func() time.Time
}

func New(token string, webhookSecret []byte) *Client {
	return newClient(defaultBaseURL, token, webhookSecret, &http.Client{Timeout: 15 * time.Second})
}

func newClient(baseURL, token string, webhookSecret []byte, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		httpClient:    httpClient,
		baseURL:       strings.TrimRight(baseURL, "/"),
		token:         strings.TrimSpace(token),
		webhookSecret: append([]byte(nil), webhookSecret...),
		now:           time.Now,
	}
}

func (c *Client) Provider() scm.Provider { return scm.GitHub }

func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	if err := c.requireToken(); err != nil {
		return "", err
	}
	var response struct {
		Login string `json:"login"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/user", nil, nil, &response); err != nil {
		return "", err
	}
	if response.Login == "" {
		return "", errors.New("github returned an empty user login")
	}
	return response.Login, nil
}

func (c *Client) ListRepositories(ctx context.Context) ([]scm.Repository, error) {
	if err := c.requireToken(); err != nil {
		return nil, err
	}
	repositories := make([]scm.Repository, 0)
	for page := 1; page <= 100; page++ {
		query := url.Values{
			"affiliation": {"owner,collaborator,organization_member"},
			"per_page":    {"100"},
			"page":        {strconv.Itoa(page)},
			"sort":        {"updated"},
		}
		var response []repositoryResponse
		if err := c.doJSON(ctx, http.MethodGet, "/user/repos", query, nil, &response); err != nil {
			return nil, err
		}
		for _, repository := range response {
			repositories = append(repositories, repository.toSCM())
		}
		if len(response) < 100 {
			return repositories, nil
		}
	}
	return nil, errors.New("github repository pagination exceeded 100 pages")
}

func (c *Client) GetFile(ctx context.Context, repository scm.Repository, path, ref string) ([]byte, error) {
	if err := c.requireToken(); err != nil {
		return nil, err
	}
	escapedPath, err := escapeRepositoryPath(path)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	if ref != "" {
		query.Set("ref", ref)
	}
	endpoint := repositoryEndpoint(repository) + "/contents/" + escapedPath
	return c.doBytes(ctx, http.MethodGet, endpoint, query, "application/vnd.github.raw+json")
}

func (c *Client) CreateWebhook(ctx context.Context, repository scm.Repository, callbackURL string, events []scm.EventType) error {
	if err := c.requireToken(); err != nil {
		return err
	}
	if len(c.webhookSecret) < 16 {
		return errors.New("github webhook secret must contain at least 16 bytes")
	}
	parsedURL, err := url.Parse(callbackURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil {
		return errors.New("github webhook callback must be an absolute HTTPS URL without user info")
	}
	githubEvents := make([]string, 0, 2)
	seen := make(map[string]bool)
	for _, event := range events {
		name := ""
		switch event {
		case scm.EventPush, scm.EventTag:
			name = "push"
		case scm.EventPullRequest:
			name = "pull_request"
		default:
			return fmt.Errorf("unsupported github webhook event %q", event)
		}
		if !seen[name] {
			seen[name] = true
			githubEvents = append(githubEvents, name)
		}
	}
	if len(githubEvents) == 0 {
		return errors.New("at least one github webhook event is required")
	}
	payload := map[string]any{
		"name":   "web",
		"active": true,
		"events": githubEvents,
		"config": map[string]string{
			"url":          callbackURL,
			"content_type": "json",
			"insecure_ssl": "0",
			"secret":       string(c.webhookSecret),
		},
	}
	return c.doJSON(ctx, http.MethodPost, repositoryEndpoint(repository)+"/hooks", nil, payload, nil)
}

func (c *Client) SetCommitStatus(ctx context.Context, repository scm.Repository, status scm.CommitStatus) error {
	if err := c.requireToken(); err != nil {
		return err
	}
	if strings.TrimSpace(status.SHA) == "" || strings.TrimSpace(status.Context) == "" {
		return errors.New("github commit status requires sha and context")
	}
	switch status.State {
	case "error", "failure", "pending", "success":
	default:
		return fmt.Errorf("invalid github commit status state %q", status.State)
	}
	payload := map[string]string{
		"state":       status.State,
		"context":     status.Context,
		"description": status.Description,
		"target_url":  status.TargetURL,
	}
	endpoint := repositoryEndpoint(repository) + "/statuses/" + url.PathEscape(status.SHA)
	return c.doJSON(ctx, http.MethodPost, endpoint, nil, payload, nil)
}

// CreatePipelineChange creates branch from the default branch, writes
// .yuanci.yml and opens a pull request. branch and title are user-visible.
func (c *Client) CreatePipelineChange(ctx context.Context, repository scm.Repository, branch, title string, content io.Reader) (scm.PullRequest, error) {
	if err := c.requireToken(); err != nil {
		return scm.PullRequest{}, err
	}
	if err := validateBranch(branch); err != nil {
		return scm.PullRequest{}, err
	}
	if strings.TrimSpace(title) == "" || repository.DefaultBranch == "" {
		return scm.PullRequest{}, errors.New("github pipeline change requires title and repository default branch")
	}
	body, err := io.ReadAll(io.LimitReader(content, maxPipeline+1))
	if err != nil {
		return scm.PullRequest{}, fmt.Errorf("read pipeline content: %w", err)
	}
	if len(body) > maxPipeline {
		return scm.PullRequest{}, errors.New("pipeline content exceeds 1 MiB")
	}

	var reference struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	refPath := repositoryEndpoint(repository) + "/git/ref/heads/" + url.PathEscape(repository.DefaultBranch)
	if err := c.doJSON(ctx, http.MethodGet, refPath, nil, nil, &reference); err != nil {
		return scm.PullRequest{}, fmt.Errorf("resolve default branch: %w", err)
	}
	if reference.Object.SHA == "" {
		return scm.PullRequest{}, errors.New("github returned an empty default branch SHA")
	}
	if err := c.doJSON(ctx, http.MethodPost, repositoryEndpoint(repository)+"/git/refs", nil, map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": reference.Object.SHA,
	}, nil); err != nil {
		return scm.PullRequest{}, fmt.Errorf("create pipeline branch: %w", err)
	}
	if err := c.doJSON(ctx, http.MethodPut, repositoryEndpoint(repository)+"/contents/.yuanci.yml", nil, map[string]string{
		"message": title,
		"content": base64.StdEncoding.EncodeToString(body),
		"branch":  branch,
	}, nil); err != nil {
		return scm.PullRequest{}, fmt.Errorf("write pipeline configuration: %w", err)
	}
	var response struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := c.doJSON(ctx, http.MethodPost, repositoryEndpoint(repository)+"/pulls", nil, map[string]string{
		"title": title,
		"head":  branch,
		"base":  repository.DefaultBranch,
	}, &response); err != nil {
		return scm.PullRequest{}, fmt.Errorf("create pipeline pull request: %w", err)
	}
	return scm.PullRequest{
		Number: response.Number, Title: response.Title, Head: response.Head.Ref,
		Base: response.Base.Ref, SHA: response.Head.SHA, URL: response.HTMLURL,
	}, nil
}

func (c *Client) ParseWebhook(headers map[string][]string, body []byte) (scm.Event, error) {
	signature := header(headers, "X-Hub-Signature-256")
	if err := webhook.VerifyHMACSHA256(c.webhookSecret, body, signature, "sha256="); err != nil {
		return scm.Event{}, fmt.Errorf("%w: %v", scm.ErrInvalidHook, err)
	}
	deliveryID := strings.TrimSpace(header(headers, "X-GitHub-Delivery"))
	if deliveryID == "" {
		return scm.Event{}, fmt.Errorf("%w: X-GitHub-Delivery is required", scm.ErrInvalidHook)
	}
	eventName := strings.TrimSpace(header(headers, "X-GitHub-Event"))
	switch eventName {
	case "push":
		return c.parsePush(deliveryID, body)
	case "pull_request":
		return c.parsePullRequest(deliveryID, body)
	default:
		return scm.Event{}, fmt.Errorf("%w: github event %q", scm.ErrUnsupportedEvent, eventName)
	}
}

func (c *Client) parsePush(deliveryID string, body []byte) (scm.Event, error) {
	var payload struct {
		Ref        string             `json:"ref"`
		Before     string             `json:"before"`
		After      string             `json:"after"`
		Repository repositoryResponse `json:"repository"`
		Sender     struct {
			Login string `json:"login"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return scm.Event{}, fmt.Errorf("%w: decode github push: %v", scm.ErrInvalidHook, err)
	}
	eventType := scm.EventPush
	if strings.HasPrefix(payload.Ref, "refs/tags/") {
		eventType = scm.EventTag
	}
	return scm.Event{
		Provider: scm.GitHub, DeliveryID: deliveryID, Type: eventType,
		Repository: payload.Repository.toSCM(), Ref: payload.Ref,
		BeforeSHA: payload.Before, AfterSHA: payload.After, Sender: payload.Sender.Login,
		ReceivedAt: c.now().UTC(),
	}, nil
}

func (c *Client) parsePullRequest(deliveryID string, body []byte) (scm.Event, error) {
	var payload struct {
		Action     string             `json:"action"`
		Number     int                `json:"number"`
		Repository repositoryResponse `json:"repository"`
		Sender     struct {
			Login string `json:"login"`
		} `json:"sender"`
		PullRequest struct {
			Head struct {
				Ref  string              `json:"ref"`
				SHA  string              `json:"sha"`
				Repo *repositoryResponse `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return scm.Event{}, fmt.Errorf("%w: decode github pull request: %v", scm.ErrInvalidHook, err)
	}
	metadata := map[string]string{
		"number": strconv.Itoa(payload.Number), "head_ref": payload.PullRequest.Head.Ref,
		"base_ref": payload.PullRequest.Base.Ref,
	}
	if payload.PullRequest.Head.Repo != nil {
		metadata["head_repository_id"] = payload.PullRequest.Head.Repo.ID.String()
		metadata["fork"] = strconv.FormatBool(payload.PullRequest.Head.Repo.ID.String() != payload.Repository.ID.String())
	}
	return scm.Event{
		Provider: scm.GitHub, DeliveryID: deliveryID, Type: scm.EventPullRequest,
		Action: payload.Action, Repository: payload.Repository.toSCM(),
		Ref:      "refs/pull/" + strconv.Itoa(payload.Number) + "/head",
		AfterSHA: payload.PullRequest.Head.SHA, Sender: payload.Sender.Login,
		ReceivedAt: c.now().UTC(), Metadata: metadata,
	}, nil
}

type repositoryResponse struct {
	ID            json.Number `json:"id"`
	Name          string      `json:"name"`
	CloneURL      string      `json:"clone_url"`
	HTMLURL       string      `json:"html_url"`
	DefaultBranch string      `json:"default_branch"`
	Private       bool        `json:"private"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (r repositoryResponse) toSCM() scm.Repository {
	return scm.Repository{
		ExternalID: r.ID.String(), Owner: r.Owner.Login, Name: r.Name,
		CloneURL: r.CloneURL, WebURL: r.HTMLURL, DefaultBranch: r.DefaultBranch, Private: r.Private,
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, requestBody, responseBody any) error {
	var encoded []byte
	var err error
	if requestBody != nil {
		encoded, err = json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode github request: %w", err)
		}
	}
	response, err := c.do(ctx, method, path, query, encoded, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := githubError(response); err != nil {
		return err
	}
	if responseBody == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponse))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponse+1))
	decoder.UseNumber()
	if err := decoder.Decode(responseBody); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}

func (c *Client) doBytes(ctx context.Context, method, path string, query url.Values, accept string) ([]byte, error) {
	response, err := c.do(ctx, method, path, query, nil, accept)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if err := githubError(response); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read github response: %w", err)
	}
	if len(body) > maxResponse {
		return nil, errors.New("github response exceeds 10 MiB")
	}
	return body, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, accept string) (*http.Response, error) {
	endpoint, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("build github URL: %w", err)
	}
	endpoint.RawQuery = query.Encode()
	maxAttempts := 1
	if method == http.MethodGet || method == http.MethodHead {
		maxAttempts = 3
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create github request: %w", err)
		}
		request.Header.Set("Accept", accept)
		request.Header.Set("X-GitHub-Api-Version", apiVersion)
		request.Header.Set("User-Agent", "YuanCI/0.1")
		if c.token != "" {
			request.Header.Set("Authorization", "Bearer "+c.token)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := c.httpClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("github request: %w", err)
		}
		if response.StatusCode != http.StatusBadGateway && response.StatusCode != http.StatusServiceUnavailable && response.StatusCode != http.StatusGatewayTimeout {
			return response, nil
		}
		if attempt == maxAttempts-1 {
			return response, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("github request retry loop exhausted")
}

func githubError(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	detail := strings.TrimSpace(string(message))
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: HTTP %d", scm.ErrUnauthorized, response.StatusCode)
	case http.StatusForbidden:
		if response.Header.Get("X-RateLimit-Remaining") == "0" {
			return fmt.Errorf("%w: HTTP %d", scm.ErrRateLimited, response.StatusCode)
		}
		return fmt.Errorf("github request forbidden: HTTP %d", response.StatusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: HTTP %d", scm.ErrRateLimited, response.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("%w: HTTP %d", scm.ErrNotFound, response.StatusCode)
	default:
		if detail == "" {
			detail = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("github API HTTP %d: %s", response.StatusCode, detail)
	}
}

func (c *Client) requireToken() error {
	if c.token == "" {
		return scm.ErrUnauthorized
	}
	return nil
}

func repositoryEndpoint(repository scm.Repository) string {
	return "/repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
}

func escapeRepositoryPath(path string) (string, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")
	if path == "" {
		return "", errors.New("repository file path is required")
	}
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("repository file path contains an unsafe segment")
		}
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), nil
}

func validateBranch(branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "@" || strings.HasPrefix(branch, "refs/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") {
		return errors.New("invalid pipeline branch name")
	}
	if strings.ContainsAny(branch, " ~^:?*[\\") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return errors.New("invalid pipeline branch name")
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return errors.New("invalid pipeline branch name")
		}
	}
	return nil
}

func header(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

var _ scm.Adapter = (*Client)(nil)
