// Package integration coordinates administrator-owned SCM discovery and import.
package integration

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/secrets"
	"time"
)

var ErrConfig = errors.New("invalid App configuration")
var ErrStale = errors.New("authorization expired or configuration changed")
var ErrRemote = errors.New("GitHub request failed")
var ErrRate = errors.New("GitHub rate limit reached")
var ErrAccess = errors.New("GitHub access could not be verified")

type App struct {
	LoginID  uuid.UUID        `json:"-"`
	ID       uuid.UUID        `json:"id"`
	AppID    string           `json:"app_id"`
	Slug     string           `json:"slug"`
	ClientID string           `json:"client_id"`
	Key      secrets.Envelope `json:"-"`
}
type Proof struct {
	ID        uuid.UUID
	Token     secrets.Envelope
	ExpiresAt time.Time
}
type Snapshot struct {
	FlowID   uuid.UUID
	LoginID  uuid.UUID
	ClientID string
	Secret   secrets.Envelope
	Subject  string
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
