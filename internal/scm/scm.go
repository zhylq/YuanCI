package scm

import (
	"context"
	"errors"
	"io"
	"time"
)

type Provider string

const (
	GitHub Provider = "github"
	GitLab Provider = "gitlab"
	Gitea  Provider = "gitea"
	Gitee  Provider = "gitee"
)

type EventType string

const (
	EventPush        EventType = "push"
	EventPullRequest EventType = "pull_request"
	EventTag         EventType = "tag"
)

type Event struct {
	Provider   Provider          `json:"provider"`
	DeliveryID string            `json:"delivery_id"`
	Type       EventType         `json:"type"`
	Action     string            `json:"action,omitempty"`
	Repository Repository        `json:"repository"`
	Ref        string            `json:"ref,omitempty"`
	BeforeSHA  string            `json:"before_sha,omitempty"`
	AfterSHA   string            `json:"after_sha,omitempty"`
	Sender     string            `json:"sender"`
	ReceivedAt time.Time         `json:"received_at"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type Repository struct {
	ExternalID    string `json:"external_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	CloneURL      string `json:"clone_url"`
	WebURL        string `json:"web_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

type CommitStatus struct {
	SHA         string
	Context     string
	State       string
	Description string
	TargetURL   string
}

type PullRequest struct {
	Number int
	Title  string
	Head   string
	Base   string
	SHA    string
	URL    string
}

type Adapter interface {
	Provider() Provider
	CurrentUser(context.Context) (string, error)
	ListRepositories(context.Context) ([]Repository, error)
	GetFile(context.Context, Repository, string, string) ([]byte, error)
	CreateWebhook(context.Context, Repository, string, []EventType) error
	SetCommitStatus(context.Context, Repository, CommitStatus) error
	CreatePipelineChange(context.Context, Repository, string, string, io.Reader) (PullRequest, error)
	ParseWebhook(headers map[string][]string, body []byte) (Event, error)
}

var (
	ErrUnauthorized     = errors.New("SCM token is unauthorized")
	ErrRateLimited      = errors.New("SCM request is rate limited")
	ErrNotFound         = errors.New("SCM resource was not found")
	ErrInvalidHook      = errors.New("SCM webhook signature is invalid")
	ErrUnsupportedEvent = errors.New("SCM webhook event is unsupported")
)
