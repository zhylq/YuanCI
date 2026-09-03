// Package githubapp provides short-lived, repository-scoped GitHub App access
// for trusted control-plane workers.
package githubapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

var (
	ErrInvalidEvent          = errors.New("invalid GitHub event")
	ErrExternalFork          = errors.New("external fork pull request is not trusted")
	ErrRepositoryUnavailable = errors.New("GitHub repository is not available")
	ErrCredentialUnavailable = errors.New("GitHub repository credential is not available")
	shaPattern               = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

type Repository struct {
	ID             uuid.UUID
	ExternalID     string
	Owner          string
	Name           string
	CloneURL       string
	DefaultBranch  string
	InstallationID string
	AppID          uuid.UUID
	AppClientID    string
	EncryptedKey   secrets.Envelope
}

type Store interface {
	ResolveGitHubRepository(context.Context, string) (Repository, error)
}

type Provider interface {
	InstallationToken(context.Context, string, []byte, string, string) ([]byte, time.Time, error)
	RepositoryCommit(context.Context, []byte, string, string, string) (string, error)
	RepositoryFile(context.Context, []byte, string, string, string, string) ([]byte, error)
}

type ValidationProof struct {
	RepositoryID uuid.UUID
	AppRevision  uuid.UUID
	CommitSHA    string
	ConfigSHA256 string
	PipelineName string
}

// CheckoutCredential is an ephemeral token bound to one trusted GitHub
// repository. The caller owns Token and must clear it after delivery or use.
type CheckoutCredential struct {
	RepositoryID string
	Token        []byte
	ExpiresAt    time.Time
}

type Service struct {
	store    Store
	cipher   *secrets.Cipher
	provider Provider
	now      func() time.Time
}

func New(store Store, cipher *secrets.Cipher, provider Provider) (*Service, error) {
	if store == nil || cipher == nil || provider == nil {
		return nil, errors.New("GitHub App service requires store, cipher and provider")
	}
	return &Service{store: store, cipher: cipher, provider: provider, now: time.Now}, nil
}

// KeyAAD is the stable associated-data contract for encrypted GitHub App keys.
func KeyAAD(id uuid.UUID) []byte { return []byte("yuanci:github-app:" + id.String()) }

// IssueCheckoutCredential mints a repository-scoped contents:read token for a
// locally trusted repository binding. The local and provider identifiers must
// both match the same active repository record.
func (s *Service) IssueCheckoutCredential(ctx context.Context, repositoryID uuid.UUID, externalID string) (CheckoutCredential, error) {
	fail := func(err error) (CheckoutCredential, error) { return CheckoutCredential{}, err }
	if repositoryID == uuid.Nil || !identity.ValidGitHubSubject(externalID) {
		return fail(ErrRepositoryUnavailable)
	}
	repository, err := s.store.ResolveGitHubRepository(ctx, externalID)
	if err != nil {
		return fail(err)
	}
	if repository.ID != repositoryID || repository.ExternalID != externalID {
		return fail(ErrRepositoryUnavailable)
	}
	key, err := s.cipher.Open(repository.EncryptedKey, KeyAAD(repository.AppID))
	if err != nil {
		return fail(ErrCredentialUnavailable)
	}
	defer clear(key)
	token, expiry, err := s.provider.InstallationToken(ctx, repository.AppClientID, key,
		repository.InstallationID, repository.ExternalID)
	if err != nil {
		clear(token)
		return fail(fmt.Errorf("mint GitHub checkout token: %w", err))
	}
	now := s.now()
	if len(token) == 0 || len(token) > 8192 || bytes.IndexFunc(token, func(r rune) bool {
		return r == ' ' || r == '\r' || r == '\n' || r == 0
	}) >= 0 || !expiry.After(now.Add(30*time.Second)) || expiry.After(now.Add(65*time.Minute)) {
		clear(token)
		return fail(ErrCredentialUnavailable)
	}
	return CheckoutCredential{RepositoryID: repository.ExternalID, Token: token, ExpiresAt: expiry}, nil
}

// FetchPipeline reads only from the immutable event commit. The returned
// repository is resolved from local trusted state, never from webhook URLs.
func (s *Service) FetchPipeline(ctx context.Context, event scm.Event, pipelinePath string) (Repository, []byte, error) {
	if event.Provider != scm.GitHub || !identity.ValidGitHubSubject(event.Repository.ExternalID) || !shaPattern.MatchString(event.AfterSHA) {
		return Repository{}, nil, ErrInvalidEvent
	}
	if event.Type != scm.EventPush && event.Type != scm.EventTag && event.Type != scm.EventPullRequest {
		return Repository{}, nil, ErrInvalidEvent
	}
	if event.Type == scm.EventPullRequest && event.Metadata["fork"] != "false" {
		return Repository{}, nil, ErrExternalFork
	}
	if err := project.ValidatePipelinePath(pipelinePath); err != nil {
		return Repository{}, nil, err
	}
	repository, err := s.store.ResolveGitHubRepository(ctx, event.Repository.ExternalID)
	if err != nil {
		return Repository{}, nil, err
	}
	key, err := s.cipher.Open(repository.EncryptedKey, KeyAAD(repository.AppID))
	if err != nil {
		return Repository{}, nil, ErrCredentialUnavailable
	}
	defer clear(key)
	token, expiry, err := s.provider.InstallationToken(ctx, repository.AppClientID, key,
		repository.InstallationID, repository.ExternalID)
	if err != nil {
		return Repository{}, nil, fmt.Errorf("mint GitHub installation token: %w", err)
	}
	defer clear(token)
	now := s.now()
	if len(token) == 0 || !expiry.After(now.Add(30*time.Second)) || expiry.After(now.Add(65*time.Minute)) {
		return Repository{}, nil, ErrCredentialUnavailable
	}
	content, err := s.provider.RepositoryFile(ctx, token, repository.Owner, repository.Name, pipelinePath, event.AfterSHA)
	if err != nil {
		return Repository{}, nil, fmt.Errorf("read GitHub pipeline at event commit: %w", err)
	}
	return repository, content, nil
}

// ValidateDefaultPipeline resolves the mutable default branch once, then reads
// and compiles the configuration only at the returned immutable commit SHA.
func (s *Service) ValidateDefaultPipeline(ctx context.Context, externalID, pipelinePath string) (ValidationProof, error) {
	if !identity.ValidGitHubSubject(externalID) || project.ValidatePipelinePath(pipelinePath) != nil {
		return ValidationProof{}, ErrInvalidEvent
	}
	repository, err := s.store.ResolveGitHubRepository(ctx, externalID)
	if err != nil {
		return ValidationProof{}, err
	}
	if repository.DefaultBranch == "" || len(repository.DefaultBranch) > 255 || strings.ContainsAny(repository.DefaultBranch, "\r\n\x00") {
		return ValidationProof{}, ErrRepositoryUnavailable
	}
	key, err := s.cipher.Open(repository.EncryptedKey, KeyAAD(repository.AppID))
	if err != nil {
		return ValidationProof{}, ErrCredentialUnavailable
	}
	defer clear(key)
	token, expiry, err := s.provider.InstallationToken(ctx, repository.AppClientID, key,
		repository.InstallationID, repository.ExternalID)
	if err != nil {
		return ValidationProof{}, fmt.Errorf("mint GitHub installation token: %w", err)
	}
	defer clear(token)
	now := s.now()
	if len(token) == 0 || !expiry.After(now.Add(30*time.Second)) || expiry.After(now.Add(65*time.Minute)) {
		return ValidationProof{}, ErrCredentialUnavailable
	}
	commitSHA, err := s.provider.RepositoryCommit(ctx, token, repository.Owner, repository.Name, repository.DefaultBranch)
	if err != nil || !shaPattern.MatchString(commitSHA) {
		if err == nil {
			err = ErrRepositoryUnavailable
		}
		return ValidationProof{}, fmt.Errorf("resolve GitHub default branch: %w", err)
	}
	commitSHA = strings.ToLower(commitSHA)
	content, err := s.provider.RepositoryFile(ctx, token, repository.Owner, repository.Name, pipelinePath, commitSHA)
	if err != nil {
		return ValidationProof{}, fmt.Errorf("read GitHub pipeline at validated commit: %w", err)
	}
	plan, err := pipeline.Compile(content, now)
	if err != nil {
		return ValidationProof{}, err
	}
	return ValidationProof{RepositoryID: repository.ID, AppRevision: repository.AppID, CommitSHA: commitSHA,
		ConfigSHA256: plan.ConfigSHA256, PipelineName: plan.Name}, nil
}
