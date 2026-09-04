package gitee

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

type Binding struct {
	Repository
	ProjectID    uuid.UUID
	GrantID      uuid.UUID
	HookRevision int64
	HookSecret   secrets.Envelope
}
type AutomationStore interface {
	githubhook.Store
	ResolveGiteeRepository(context.Context, string) (Binding, error)
	GiteeProject(context.Context, string, uuid.UUID) (Binding, error)
	SaveGiteeWebhook(context.Context, string, Binding, secrets.Envelope) error
	GiteeValidationTarget(context.Context, string, uuid.UUID, int64) (Binding, project.AutomationSettings, error)
	RecordGiteeValidation(context.Context, string, Binding, project.AutomationSettings, githubapp.ValidationProof) error
}

func hookAAD(id uuid.UUID, revision int64) []byte {
	return []byte("yuanci:gitee:webhook:" + id.String() + ":" + strconv.FormatInt(revision, 10))
}
func (s *Service) WebhookSettings(ctx context.Context, session string, id uuid.UUID) (string, int64, error) {
	store, ok := s.Store.(AutomationStore)
	if !ok {
		return "", 0, ErrStale
	}
	binding, err := store.GiteeProject(ctx, session, id)
	if err != nil {
		return "", 0, err
	}
	return s.Origin + "/api/v1/webhooks/gitee/" + binding.ID, binding.HookRevision, nil
}
func (s *Service) SaveWebhook(ctx context.Context, session string, id uuid.UUID, revision int64, secret []byte) error {
	if len(secret) < 32 || len(secret) > 4096 || strings.ContainsAny(string(secret), "\r\n\x00") {
		return project.ErrAutomationInvalid
	}
	store, ok := s.Store.(AutomationStore)
	if !ok {
		return ErrStale
	}
	binding, err := store.GiteeProject(ctx, session, id)
	if err != nil {
		return err
	}
	if revision != binding.HookRevision {
		return project.ErrAutomationConflict
	}
	encrypted, err := s.cipher.Seal(secret, hookAAD(id, revision+1))
	if err != nil {
		return ErrStale
	}
	return store.SaveGiteeWebhook(ctx, session, binding, encrypted)
}

type hookLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (l *hookLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.last.IsZero() {
		l.tokens = 100
	} else {
		l.tokens += now.Sub(l.last).Seconds() * 50
		if l.tokens > 100 {
			l.tokens = 100
		}
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
func (s *Service) ReceiveWebhook(ctx context.Context, external string, headers http.Header, body []byte) (githubhook.Receipt, error) {
	if !s.hooks.allow() {
		return githubhook.Receipt{}, githubhook.ErrRateLimited
	}
	store, ok := s.Store.(AutomationStore)
	if !ok || len(body) > 2<<20 {
		return githubhook.Receipt{}, scm.ErrInvalidHook
	}
	binding, err := store.ResolveGiteeRepository(ctx, external)
	if err != nil || binding.HookRevision == 0 {
		return githubhook.Receipt{}, scm.ErrInvalidHook
	}
	secret, err := s.cipher.Open(binding.HookSecret, hookAAD(binding.ProjectID, binding.HookRevision))
	if err != nil {
		return githubhook.Receipt{}, scm.ErrInvalidHook
	}
	defer clear(secret)
	event, err := NormalizeWebhook(headers, body, secret, binding.Repository, time.Now())
	if err != nil {
		return githubhook.Receipt{}, err
	}
	event.Metadata["webhook_revision"] = strconv.FormatInt(binding.HookRevision, 10)
	normalized, err := json.Marshal(event)
	if err != nil {
		return githubhook.Receipt{}, scm.ErrInvalidHook
	}
	return store.ReceiveWebhook(ctx, githubhook.Delivery{Provider: "gitee", ProviderInstance: "https://gitee.com", DeliveryID: event.DeliveryID, EventType: string(event.Type), PayloadSHA256: event.DeliveryID, NormalizedEvent: normalized, ReceivedAt: event.ReceivedAt})
}
func (s *Service) FetchPipeline(ctx context.Context, event scm.Event, path string) (githubapp.Repository, []byte, error) {
	if event.Provider != scm.Gitee || !shaPattern.MatchString(event.AfterSHA) || project.ValidatePipelinePath(path) != nil {
		return githubapp.Repository{}, nil, githubapp.ErrInvalidEvent
	}
	if event.Type == scm.EventPullRequest && event.Metadata["fork"] != "false" {
		return githubapp.Repository{}, nil, githubapp.ErrExternalFork
	}
	store, ok := s.Store.(AutomationStore)
	if !ok {
		return githubapp.Repository{}, nil, githubapp.ErrRepositoryUnavailable
	}
	provider, ok := s.Provider.(PipelineProvider)
	if !ok {
		return githubapp.Repository{}, nil, ErrRemote
	}
	binding, err := store.ResolveGiteeRepository(ctx, event.Repository.ExternalID)
	if err != nil {
		return githubapp.Repository{}, nil, githubapp.ErrRepositoryUnavailable
	}
	if strconv.FormatInt(binding.HookRevision, 10) != event.Metadata["webhook_revision"] {
		return githubapp.Repository{}, nil, githubapp.ErrInvalidEvent
	}
	token, err := s.Access(ctx, binding.GrantID)
	if err != nil {
		return githubapp.Repository{}, nil, err
	}
	defer clear(token)
	if err := provider.VerifyEvent(ctx, string(token), binding.Repository, event); err != nil {
		return githubapp.Repository{}, nil, err
	}
	content, err := provider.File(ctx, string(token), binding.Repository, path, event.AfterSHA)
	if err != nil {
		return githubapp.Repository{}, nil, err
	}
	return githubapp.Repository{ID: binding.ProjectID, ExternalID: binding.ID, Owner: binding.Owner, Name: binding.Name, CloneURL: "https://gitee.com/" + binding.Owner + "/" + binding.Name + ".git"}, content, nil
}
func (s *Service) ValidateProject(ctx context.Context, session string, id uuid.UUID, revision int64) (githubapp.ValidationProof, error) {
	store, ok := s.Store.(AutomationStore)
	if !ok {
		return githubapp.ValidationProof{}, ErrStale
	}
	provider, ok := s.Provider.(PipelineProvider)
	if !ok {
		return githubapp.ValidationProof{}, ErrRemote
	}
	binding, settings, err := store.GiteeValidationTarget(ctx, session, id, revision)
	if err != nil {
		return githubapp.ValidationProof{}, err
	}
	token, err := s.Access(ctx, binding.GrantID)
	if err != nil {
		return githubapp.ValidationProof{}, err
	}
	defer clear(token)
	grant, _, err := s.Store.GiteeGrant(ctx, binding.GrantID)
	if err != nil {
		return githubapp.ValidationProof{}, err
	}
	// Pin credential revision before external validation; persistence rechecks it.
	plain, err := s.cipher.Open(grant.Encrypted, GrantAAD(grant))
	if err != nil {
		return githubapp.ValidationProof{}, ErrStale
	}
	defer clear(plain)
	var material Token
	if json.Unmarshal(plain, &material) != nil {
		return githubapp.ValidationProof{}, ErrStale
	}
	repo, err := provider.Repository(ctx, material.Access, binding.Owner, binding.Name)
	if err != nil {
		return githubapp.ValidationProof{}, err
	}
	if repo.ID != binding.ID {
		return githubapp.ValidationProof{}, ErrStale
	}
	sha, err := provider.Commit(ctx, material.Access, repo, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return githubapp.ValidationProof{}, err
	}
	content, err := provider.File(ctx, material.Access, repo, settings.PipelinePath, sha)
	if err != nil {
		return githubapp.ValidationProof{}, err
	}
	plan, err := pipeline.Compile(content, time.Now())
	if err != nil {
		return githubapp.ValidationProof{}, err
	}
	proof := githubapp.ValidationProof{RepositoryID: id, AppRevision: grant.Revision, CommitSHA: sha, ConfigSHA256: plan.ConfigSHA256, PipelineName: plan.Name}
	if err := store.RecordGiteeValidation(ctx, session, binding, settings, proof); err != nil {
		return githubapp.ValidationProof{}, err
	}
	return proof, nil
}
