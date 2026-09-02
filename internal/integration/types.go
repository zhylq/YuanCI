// Package integration coordinates administrator-owned SCM discovery and import.
package integration

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/secrets"
)

var ErrConfig = errors.New("invalid App configuration")
var ErrStale = errors.New("authorization expired or configuration changed")
var ErrRemote = errors.New("GitHub request failed")
var ErrRate = errors.New("GitHub rate limit reached")
var ErrAccess = errors.New("GitHub access could not be verified")
var ErrWebhookUnavailable = errors.New("GitHub webhook is not configured")

type App struct {
	LoginID              uuid.UUID        `json:"-"`
	ID                   uuid.UUID        `json:"id"`
	AppID                string           `json:"app_id"`
	Slug                 string           `json:"slug"`
	ClientID             string           `json:"client_id"`
	Key                  secrets.Envelope `json:"-"`
	WebhookSecret        secrets.Envelope `json:"-"`
	WebhookSecretPresent bool             `json:"-"`
	WebhookEnabled       bool             `json:"webhook_enabled"`
	WebhookSecretVersion int64            `json:"-"`
}
type Proof struct {
	ID        uuid.UUID
	Subject   string
	Token     secrets.Envelope
	ExpiresAt time.Time
}
type Snapshot struct {
	FlowID   uuid.UUID
	LoginID  uuid.UUID
	ClientID string
	Secret   secrets.Envelope
	Subjects []string
	App      *App
	Proof    *Proof
}
type Installation struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	Account   string `json:"account"`
}
type Repository struct {
	ID            string `json:"id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
}
type RepoPage struct {
	Items    []Repository `json:"items"`
	NextPage int          `json:"next_page,omitempty"`
}
type Imported struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Created bool      `json:"created"`
}
type RepositoryStore interface {
	IntegrationContext(context.Context, string, bool) (Snapshot, error)
	SaveIntegrationApp(context.Context, string, Snapshot, App) error
	WebhookIntegration(context.Context) (App, error)
	BeginIntegrationFlow(context.Context, string, Snapshot, string, string) error
	ConsumeIntegrationFlow(context.Context, string, string, string) (Snapshot, error)
	SaveIntegrationProof(context.Context, string, Snapshot, Proof) error
	CheckIntegration(context.Context, string, Snapshot, bool) error
	ImportRepositories(context.Context, string, Snapshot, Installation, []Repository) ([]Imported, error)
}
type Provider interface {
	VerifyApp(context.Context, string, []byte) (App, error)
	AuthorizationURL(string, string, string, string) string
	Exchange(context.Context, string, string, string, string, string) (string, string, time.Time, error)
	Installations(context.Context, string) ([]Installation, error)
	VerifyInstallation(context.Context, App, []byte, Installation) error
	Repositories(context.Context, string, string, int) (RepoPage, error)
}
