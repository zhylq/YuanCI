package integration

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/secrets"
)

type Service struct {
	Repo     RepositoryStore
	Provider Provider
	cipher   *secrets.Cipher
	origin   string
}

func New(repo RepositoryStore, cipher *secrets.Cipher, origin string) *Service {
	return &Service{Repo: repo, Provider: NewGitHub(), cipher: cipher, origin: strings.TrimRight(origin, "/")}
}
func (s *Service) CallbackURL() string { return s.origin + "/api/v1/integrations/github/callback" }
func (s *Service) WebhookURL() string  { return s.origin + "/api/v1/webhooks/github" }

type Settings struct {
	NeedsVerification       bool       `json:"needs_verification"`
	App                     *App       `json:"app"`
	CallbackURL             string     `json:"callback_url"`
	SetupURL                string     `json:"setup_url"`
	InstallURL              string     `json:"install_url,omitempty"`
	AuthorizedUntil         *time.Time `json:"authorized_until,omitempty"`
	WebhookURL              string     `json:"webhook_url"`
	WebhookSecretConfigured bool       `json:"webhook_secret_configured"`
}

func (s *Service) Settings(ctx context.Context, token string) (Settings, error) {
	snap, err := s.Repo.IntegrationContext(ctx, token, false)
	if err != nil {
		return Settings{}, err
	}
	result := Settings{App: snap.App, CallbackURL: s.CallbackURL(), WebhookURL: s.WebhookURL(), SetupURL: s.origin + "/settings/repositories", NeedsVerification: snap.App != nil && snap.App.LoginID != snap.LoginID}
	if snap.App != nil {
		result.InstallURL = "https://github.com/apps/" + snap.App.Slug + "/installations/new"
		result.WebhookSecretConfigured = snap.App.WebhookSecretPresent
	}
	if snap.Proof != nil {
		result.AuthorizedUntil = &snap.Proof.ExpiresAt
	}
	return result, nil
}

type AppInput struct {
	AppID            string     `json:"app_id"`
	PrivateKey       string     `json:"private_key"`
	WebhookSecret    *string    `json:"webhook_secret,omitempty"`
	WebhookEnabled   *bool      `json:"webhook_enabled,omitempty"`
	ExpectedRevision *uuid.UUID `json:"expected_revision"`
}

func (s *Service) Save(ctx context.Context, token string, input AppInput) error {
	if !identity.ValidGitHubSubject(input.AppID) {
		return ErrConfig
	}
	snap, err := s.Repo.IntegrationContext(ctx, token, true)
	if err != nil {
		return err
	}
	if (snap.App == nil) != (input.ExpectedRevision == nil) || (snap.App != nil && *input.ExpectedRevision != snap.App.ID) {
		return ErrStale
	}
	key := []byte(input.PrivateKey)
	defer clear(key)
	app, err := s.Provider.VerifyApp(ctx, snap.ClientID, key)
	if err != nil {
		return err
	}
	if app.AppID != input.AppID || app.ClientID != snap.ClientID {
		return ErrConfig
	}
	app.ID = uuid.New()
	app.LoginID = snap.LoginID
	app.Key, err = s.cipher.Seal(key, githubapp.KeyAAD(app.ID))
	if err != nil {
		return ErrConfig
	}
	app.WebhookEnabled = false
	if snap.App != nil {
		app.WebhookEnabled = snap.App.WebhookEnabled
		app.WebhookSecretVersion = snap.App.WebhookSecretVersion
	}
	if input.WebhookEnabled != nil {
		app.WebhookEnabled = *input.WebhookEnabled
	}
	var webhookSecret []byte
	if input.WebhookSecret != nil {
		webhookSecret = []byte(*input.WebhookSecret)
		defer clear(webhookSecret)
		if len(webhookSecret) < 16 || len(webhookSecret) > 4096 || strings.ContainsAny(*input.WebhookSecret, "\r\n\x00") {
			return ErrConfig
		}
		app.WebhookSecretVersion++
	} else if snap.App != nil && snap.App.WebhookSecretPresent {
		webhookSecret, err = s.cipher.Open(snap.App.WebhookSecret, webhookAAD(snap.App.ID))
		if err != nil {
			return ErrConfig
		}
		defer clear(webhookSecret)
	}
	if len(webhookSecret) != 0 {
		app.WebhookSecret, err = s.cipher.Seal(webhookSecret, webhookAAD(app.ID))
		if err != nil {
			return ErrConfig
		}
		app.WebhookSecretPresent = true
	}
	if app.WebhookEnabled && !app.WebhookSecretPresent {
		return ErrConfig
	}
	return s.Repo.SaveIntegrationApp(ctx, token, snap, app)
}
func (s *Service) Start(ctx context.Context, token, state, nonce string) (string, error) {
	snap, err := s.Repo.IntegrationContext(ctx, token, true)
	if err != nil {
		return "", err
	}
	if snap.App == nil || snap.App.LoginID != snap.LoginID {
		return "", ErrStale
	}
	if err := s.Repo.BeginIntegrationFlow(ctx, token, snap, state, nonce); err != nil {
		return "", err
	}
	return s.Provider.AuthorizationURL(snap.ClientID, s.CallbackURL(), state, identity.PKCEVerifier(state, nonce)), nil
}
func (s *Service) Finish(ctx context.Context, token, state, nonce, code string) error {
	snap, err := s.Repo.ConsumeIntegrationFlow(ctx, token, state, nonce)
	if err != nil {
		return err
	}
	// Even a denied/empty callback consumes its flow; no token exchange on replay.
	if code == "" || len(code) > 1024 {
		return ErrAccess
	}
	secret, err := s.cipher.Open(snap.Secret, []byte("yuanci:login:github:"+snap.LoginID.String()))
	if err != nil {
		return ErrConfig
	}
	defer clear(secret)
	subject, access, expiry, err := s.Provider.Exchange(ctx, snap.ClientID, string(secret), s.CallbackURL(), code, identity.PKCEVerifier(state, nonce))
	if err != nil {
		return err
	}
	if !slices.Contains(snap.Subjects, subject) || access == "" || !expiry.After(time.Now()) {
		return ErrAccess
	}
	if max := time.Now().Add(10 * time.Minute); expiry.After(max) {
		expiry = max
	}
	proof := Proof{ID: uuid.New(), Subject: subject, ExpiresAt: expiry}
	plain := []byte(access)
	defer clear(plain)
	proof.Token, err = s.cipher.Seal(plain, proofAAD(proof.ID))
	if err != nil {
		return ErrConfig
	}
	return s.Repo.SaveIntegrationProof(ctx, token, snap, proof)
}
func proofAAD(id uuid.UUID) []byte   { return []byte("yuanci:github-proof:" + id.String()) }
func webhookAAD(id uuid.UUID) []byte { return []byte("yuanci:github-webhook:" + id.String()) }

// WebhookSecret returns a short-lived plaintext copy for request verification.
// Callers must clear it immediately after use and must never log it.
func (s *Service) WebhookSecret(ctx context.Context) ([]byte, error) {
	app, err := s.Repo.WebhookIntegration(ctx)
	if err != nil {
		return nil, err
	}
	if !app.WebhookEnabled || !app.WebhookSecretPresent {
		return nil, ErrWebhookUnavailable
	}
	plain, err := s.cipher.Open(app.WebhookSecret, webhookAAD(app.ID))
	if err != nil {
		return nil, ErrConfig
	}
	return plain, nil
}
func (s *Service) authorized(ctx context.Context, token string) (Snapshot, []byte, error) {
	snap, err := s.Repo.IntegrationContext(ctx, token, false)
	if err != nil {
		return snap, nil, err
	}
	if snap.App == nil || snap.Proof == nil {
		return snap, nil, ErrStale
	}
	plain, err := s.cipher.Open(snap.Proof.Token, proofAAD(snap.Proof.ID))
	if err != nil {
		return snap, nil, ErrConfig
	}
	return snap, plain, nil
}
func (s *Service) verifiedInstallations(ctx context.Context, snap Snapshot, access string) ([]Installation, error) {
	items, err := s.Provider.Installations(ctx, access)
	if err != nil {
		return nil, err
	}
	key, err := s.cipher.Open(snap.App.Key, githubapp.KeyAAD(snap.App.ID))
	if err != nil {
		return nil, ErrConfig
	}
	defer clear(key)
	for _, item := range items {
		if err := s.Provider.VerifyInstallation(ctx, *snap.App, key, item); err != nil {
			return nil, err
		}
	}
	return items, nil
}
func (s *Service) Installations(ctx context.Context, token string) ([]Installation, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	snap, plain, err := s.authorized(ctx, token)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	items, err := s.verifiedInstallations(ctx, snap, string(plain))
	if err != nil {
		return nil, err
	}
	if err := s.Repo.CheckIntegration(ctx, token, snap, true); err != nil {
		return nil, err
	}
	return items, nil
}
func (s *Service) installation(ctx context.Context, snap Snapshot, access, id string) (Installation, error) {
	if !identity.ValidGitHubSubject(id) {
		return Installation{}, ErrConfig
	}
	items, err := s.Provider.Installations(ctx, access)
	if err != nil {
		return Installation{}, err
	}
	for _, item := range items {
		if item.ID == id {
			key, err := s.cipher.Open(snap.App.Key, githubapp.KeyAAD(snap.App.ID))
			if err != nil {
				return Installation{}, ErrConfig
			}
			defer clear(key)
			if err := s.Provider.VerifyInstallation(ctx, *snap.App, key, item); err != nil {
				return Installation{}, err
			}
			return item, nil
		}
	}
	return Installation{}, ErrAccess
}
func (s *Service) Repositories(ctx context.Context, token, installID string, page int) (RepoPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	snap, plain, err := s.authorized(ctx, token)
	if err != nil {
		return RepoPage{}, err
	}
	defer clear(plain)
	install, err := s.installation(ctx, snap, string(plain), installID)
	if err != nil {
		return RepoPage{}, err
	}
	result, err := s.Provider.Repositories(ctx, string(plain), installID, page)
	if err != nil {
		return RepoPage{}, err
	}
	for _, repo := range result.Items {
		if !strings.EqualFold(repo.Owner, install.Account) {
			return RepoPage{}, ErrAccess
		}
	}
	if err := s.Repo.CheckIntegration(ctx, token, snap, true); err != nil {
		return RepoPage{}, err
	}
	return result, nil
}
func (s *Service) Import(ctx context.Context, token, installID string, ids []string) ([]Imported, error) {
	if len(ids) == 0 || len(ids) > 20 {
		return nil, ErrConfig
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if !identity.ValidGitHubSubject(id) || wanted[id] {
			return nil, ErrConfig
		}
		wanted[id] = true
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	snap, plain, err := s.authorized(ctx, token)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	install, err := s.installation(ctx, snap, string(plain), installID)
	if err != nil {
		return nil, err
	}
	found := map[string]Repository{}
	for page := 1; page <= 100; {
		result, err := s.Provider.Repositories(ctx, string(plain), installID, page)
		if err != nil {
			return nil, err
		}
		for _, repo := range result.Items {
			if wanted[repo.ID] && strings.EqualFold(repo.Owner, install.Account) {
				found[repo.ID] = repo
			}
		}
		if len(found) == len(wanted) || result.NextPage == 0 {
			break
		}
		if result.NextPage != page+1 {
			return nil, ErrRemote
		}
		page = result.NextPage
	}
	if len(found) != len(wanted) {
		return nil, ErrAccess
	}
	selected := make([]Repository, 0, len(ids))
	for _, id := range ids {
		selected = append(selected, found[id])
	}
	return s.Repo.ImportRepositories(ctx, token, snap, install, selected)
}
