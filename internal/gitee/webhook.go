package gitee

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
)

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

func validRef(ref string) bool {
	return refPattern.MatchString(ref) && !strings.Contains(ref, "..") && !strings.Contains(ref, "//") && !strings.HasSuffix(ref, "/") && !strings.HasSuffix(ref, ".")
}

type hookRepo struct {
	ID int64 `json:"id"`
}
type pullRequest struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Head   struct {
		SHA  string   `json:"sha"`
		Ref  string   `json:"ref"`
		Repo hookRepo `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string   `json:"ref"`
		Repo hookRepo `json:"repo"`
	} `json:"base"`
}

// NormalizeWebhook supports Gitee password mode over HTTPS. The documented
// timestamp/secret signature does not cover the body and is deliberately not
// accepted as a substitute. No password, remote URL or raw payload is retained.
func NormalizeWebhook(headers http.Header, body, secret []byte, repo Repository, now time.Time) (scm.Event, error) {
	if len(body) == 0 || len(body) > 2<<20 || len(secret) < 32 || len(secret) > 4096 || !identity.ValidGitHubSubject(repo.ID) || !ValidComponent(repo.Owner) || !ValidComponent(repo.Name) {
		return scm.Event{}, scm.ErrInvalidHook
	}
	single := func(key string) string {
		values := headers.Values(key)
		if len(values) != 1 {
			return ""
		}
		return values[0]
	}
	token := single("X-Gitee-Token")
	if subtle.ConstantTimeCompare([]byte(token), secret) != 1 {
		return scm.Event{}, scm.ErrInvalidHook
	}
	stamp, err := strconv.ParseInt(single("X-Gitee-Timestamp"), 10, 64)
	if err != nil {
		return scm.Event{}, scm.ErrInvalidHook
	}
	timestamp := time.UnixMilli(stamp)
	if timestamp.Before(now.Add(-time.Hour)) || timestamp.After(now.Add(time.Minute)) {
		return scm.Event{}, scm.ErrInvalidHook
	}
	var payload struct {
		Ref        string      `json:"ref"`
		Before     string      `json:"before"`
		After      string      `json:"after"`
		Deleted    bool        `json:"deleted"`
		Repository hookRepo    `json:"repository"`
		Pull       pullRequest `json:"pull_request"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return scm.Event{}, scm.ErrInvalidHook
	}
	event := scm.Event{Provider: scm.Gitee, Repository: scm.Repository{ExternalID: repo.ID, Owner: repo.Owner, Name: repo.Name}, Metadata: map[string]string{}}
	switch single("X-Gitee-Event") {
	case "Push Hook", "Tag Push Hook":
		if payload.Deleted || payload.After == strings.Repeat("0", 40) {
			return scm.Event{}, scm.ErrUnsupportedEvent
		}
		if strconv.FormatInt(payload.Repository.ID, 10) != repo.ID {
			return scm.Event{}, scm.ErrInvalidHook
		}
		event.Type = scm.EventPush
		if strings.HasPrefix(payload.Ref, "refs/tags/") {
			event.Type = scm.EventTag
		} else if !strings.HasPrefix(payload.Ref, "refs/heads/") {
			return scm.Event{}, scm.ErrInvalidHook
		}
		if single("X-Gitee-Event") == "Tag Push Hook" && event.Type != scm.EventTag {
			return scm.Event{}, scm.ErrInvalidHook
		}
		event.Ref = payload.Ref
		event.AfterSHA = strings.ToLower(payload.After)
		event.BeforeSHA = strings.ToLower(payload.Before)
		if event.BeforeSHA != "" && !shaPattern.MatchString(event.BeforeSHA) {
			return scm.Event{}, scm.ErrInvalidHook
		}
	case "Merge Request Hook":
		pr := payload.Pull
		if pr.State != "open" {
			return scm.Event{}, scm.ErrUnsupportedEvent
		}
		if pr.Number < 1 || pr.Number > 1e9 || strconv.FormatInt(pr.Base.Repo.ID, 10) != repo.ID || pr.Head.Repo.ID <= 0 || !validRef(pr.Head.Ref) || !validRef(pr.Base.Ref) {
			return scm.Event{}, scm.ErrInvalidHook
		}
		event.Type = scm.EventPullRequest
		event.Action = "update"
		event.Ref = "refs/heads/" + pr.Head.Ref
		event.AfterSHA = strings.ToLower(pr.Head.SHA)
		event.Metadata["fork"] = strconv.FormatBool(pr.Head.Repo.ID != pr.Base.Repo.ID)
		event.Metadata["pull_request_number"] = strconv.Itoa(pr.Number)
		event.Metadata["base_ref"] = pr.Base.Ref
	default:
		return scm.Event{}, scm.ErrUnsupportedEvent
	}
	if !validRef(event.Ref) || !shaPattern.MatchString(event.AfterSHA) {
		return scm.Event{}, scm.ErrInvalidHook
	}
	semantic, _ := json.Marshal(event)
	digest := sha256.Sum256(semantic)
	event.DeliveryID = hex.EncodeToString(digest[:])
	event.ReceivedAt = now.UTC()
	return event, nil
}

type PipelineProvider interface {
	RepositoryProvider
	Commit(context.Context, string, Repository, string) (string, error)
	File(context.Context, string, Repository, string, string) ([]byte, error)
	VerifyEvent(context.Context, string, Repository, scm.Event) error
}

func (c *Client) Commit(ctx context.Context, token string, repo Repository, ref string) (string, error) {
	if !ValidComponent(repo.Owner) || !ValidComponent(repo.Name) || !validRef(ref) {
		return "", ErrStale
	}
	var reply struct {
		SHA string `json:"sha"`
	}
	if err := c.get(ctx, "/repos/"+repo.Owner+"/"+repo.Name+"/commits/"+url.PathEscape(ref), token, &reply, 2<<20); err != nil {
		return "", err
	}
	if !shaPattern.MatchString(reply.SHA) {
		return "", ErrRemote
	}
	return strings.ToLower(reply.SHA), nil
}
func (c *Client) VerifyEvent(ctx context.Context, token string, repo Repository, event scm.Event) error {
	current, err := c.Repository(ctx, token, repo.Owner, repo.Name)
	if err != nil {
		return err
	}
	if current.ID != repo.ID || event.Repository.ExternalID != repo.ID {
		return scm.ErrInvalidHook
	}
	if event.Type == scm.EventPullRequest {
		if event.Metadata["fork"] != "false" {
			return scm.ErrUnauthorized
		}
		number, err := strconv.Atoi(event.Metadata["pull_request_number"])
		if err != nil || number < 1 {
			return scm.ErrInvalidHook
		}
		var pr pullRequest
		if err := c.get(ctx, "/repos/"+repo.Owner+"/"+repo.Name+"/pulls/"+strconv.Itoa(number), token, &pr, 2<<20); err != nil {
			return err
		}
		if pr.State != "open" || pr.Number != number || strconv.FormatInt(pr.Base.Repo.ID, 10) != repo.ID || pr.Head.Repo.ID != pr.Base.Repo.ID || strings.ToLower(pr.Head.SHA) != event.AfterSHA || "refs/heads/"+pr.Head.Ref != event.Ref || pr.Base.Ref != event.Metadata["base_ref"] {
			return scm.ErrInvalidHook
		}
		return nil
	}
	sha, err := c.Commit(ctx, token, repo, event.Ref)
	if err != nil {
		return err
	}
	if sha != event.AfterSHA {
		return scm.ErrInvalidHook
	}
	return nil
}
func (c *Client) File(ctx context.Context, token string, repo Repository, path, sha string) ([]byte, error) {
	if !ValidComponent(repo.Owner) || !ValidComponent(repo.Name) || !shaPattern.MatchString(sha) || project.ValidatePipelinePath(path) != nil {
		return nil, ErrStale
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	var reply struct {
		Type     string `json:"type"`
		Path     string `json:"path"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
		Content  string `json:"content"`
	}
	if err := c.get(ctx, "/repos/"+repo.Owner+"/"+repo.Name+"/contents/"+strings.Join(parts, "/")+"?ref="+strings.ToLower(sha), token, &reply, 2<<20); err != nil {
		return nil, err
	}
	if reply.Type != "file" || reply.Path != path || reply.Encoding != "base64" || reply.Size < 1 || reply.Size > 1<<20 {
		return nil, ErrRemote
	}
	content, err := base64.StdEncoding.DecodeString(reply.Content)
	if err != nil || int64(len(content)) != reply.Size {
		return nil, ErrRemote
	}
	return content, nil
}
