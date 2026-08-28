// Package provisioning coordinates managed login settings without owning SQL or HTTP routing.
package provisioning

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/secrets"
)

var ErrConflict = errors.New("configuration changed or expired; reload settings")
var ErrSetup = errors.New("setup credential is invalid, expired or unavailable")
var ErrConfig = errors.New("invalid GitHub configuration")

const CookieName = "__Host-yuanci_setup"
const CodeTTL = 15 * time.Minute
const SessionTTL = 30 * time.Minute

type Access struct {
	SessionToken string
	SetupToken   string
}
type Status struct {
	Initialized bool `json:"initialized"`
	Configured  bool `json:"configured"`
}
type Info struct {
	ID               uuid.UUID `json:"id"`
	ClientID         string    `json:"client_id"`
	BootstrapSubject string    `json:"bootstrap_subject"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type Config struct {
	Info
	Encrypted      secrets.Envelope `json:"-"`
	ExpectedActive *uuid.UUID       `json:"-"`
}
type Settings struct {
	Active    *Info `json:"active"`
	Candidate *Info `json:"candidate"`
}
type Input struct {
	ClientID         string     `json:"client_id"`
	ClientSecret     string     `json:"client_secret"`
	BootstrapSubject string     `json:"bootstrap_subject"`
	ExpectedActive   *uuid.UUID `json:"expected_active"`
}
type Repository interface {
	ProvisioningStatus(context.Context) (Status, error)
	ExchangeSetupCode(context.Context, string, string) error
	LoginSettings(context.Context, Access) (Settings, error)
	SaveLoginCandidate(context.Context, Access, Config) error
	BeginManagedOAuth(context.Context, string, string, string, uuid.UUID, Access) (Config, error)
	ManagedFlowConfig(context.Context, string) (Config, error)
	FinishManagedOAuth(context.Context, string, identity.ExternalUser, string, string) (identity.Credentials, error)
}
type ProviderFactory func(string, string, string) (identity.OAuthProvider, error)
type Service struct {
	Repo     Repository
	cipher   *secrets.Cipher
	Callback string
	Factory  ProviderFactory
}

func New(repo Repository, cipher *secrets.Cipher, origin string) *Service {
	return &Service{Repo: repo, cipher: cipher, Callback: origin + "/api/v1/auth/github/callback",
		Factory: func(id, secret, callback string) (identity.OAuthProvider, error) {
			return identity.NewGitHub(id, secret, callback)
		}}
}
func Cookie(token string) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(SessionTTL.Seconds())}
}
func (s *Service) Exchange(ctx context.Context, code string) (string, error) {
	token := identity.NewToken()
	if err := s.Repo.ExchangeSetupCode(ctx, code, token); err != nil {
		return "", err
	}
	return token, nil
}
func aad(id uuid.UUID) []byte { return []byte("yuanci:login:github:" + id.String()) }
func (s *Service) Save(ctx context.Context, access Access, input Input) (uuid.UUID, error) {
	if !identity.ValidGitHubSubject(input.BootstrapSubject) {
		return uuid.Nil, ErrConfig
	}
	if _, err := identity.NewGitHub(input.ClientID, input.ClientSecret, s.Callback); err != nil {
		return uuid.Nil, ErrConfig
	}
	id := uuid.New()
	plain := []byte(input.ClientSecret)
	defer clear(plain)
	encrypted, err := s.cipher.Seal(plain, aad(id))
	if err != nil {
		return uuid.Nil, err
	}
	config := Config{Info: Info{ID: id, ClientID: input.ClientID, BootstrapSubject: input.BootstrapSubject}, Encrypted: encrypted, ExpectedActive: input.ExpectedActive}
	if err := s.Repo.SaveLoginCandidate(ctx, access, config); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
func (s *Service) provider(config Config) (identity.OAuthProvider, error) {
	plain, err := s.cipher.Open(config.Encrypted, aad(config.ID))
	if err != nil {
		return nil, ErrConfig
	}
	defer clear(plain)
	return s.Factory(config.ClientID, string(plain), s.Callback)
}
func (s *Service) Start(ctx context.Context, state, nonce, linkToken string, candidate uuid.UUID, access Access) (identity.OAuthProvider, error) {
	config, err := s.Repo.BeginManagedOAuth(ctx, state, nonce, linkToken, candidate, access)
	if err != nil {
		return nil, err
	}
	return s.provider(config)
}
func (s *Service) Provider(ctx context.Context, ticket string) (identity.OAuthProvider, error) {
	config, err := s.Repo.ManagedFlowConfig(ctx, ticket)
	if err != nil {
		return nil, err
	}
	return s.provider(config)
}
