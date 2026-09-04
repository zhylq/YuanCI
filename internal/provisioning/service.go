// Package provisioning coordinates managed login settings without owning SQL or HTTP routing.
package provisioning

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/secrets"
)

var ErrConflict = errors.New("configuration changed or expired; reload settings")
var ErrSetup = errors.New("setup credential is invalid, expired or unavailable")
var ErrConfig = errors.New("invalid login configuration")

const CookieName = "__Host-yuanci_setup"
const CodeTTL = 15 * time.Minute
const SessionTTL = 30 * time.Minute

type Access struct {
	SessionToken string
	SetupToken   string
}
type Status struct {
	Provider    string `json:"provider"`
	Initialized bool   `json:"initialized"`
	Configured  bool   `json:"configured"`
}
type Info struct {
	ID               uuid.UUID `json:"id"`
	Provider         string    `json:"provider"`
	Instance         string    `json:"instance"`
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
	Provider         string     `json:"provider"`
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
	Repo         Repository
	cipher       *secrets.Cipher
	Callback     string
	Factory      ProviderFactory
	GiteeFactory ProviderFactory
}

func New(repo Repository, cipher *secrets.Cipher, origin string) *Service {
	return &Service{Repo: repo, cipher: cipher, Callback: origin + "/api/v1/auth/github/callback",
		Factory: func(id, secret, callback string) (identity.OAuthProvider, error) {
			return identity.NewGitHub(id, secret, callback)
		}, GiteeFactory: func(id, secret, callback string) (identity.OAuthProvider, error) {
			return identity.NewGitee(id, secret, callback)
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
func configAAD(config Config) []byte {
	if config.Provider == "github" {
		return aad(config.ID)
	}
	return []byte("yuanci:login:" + config.Provider + ":" + config.Instance + ":" + config.ID.String())
}
func (s *Service) CallbackFor(provider string) string {
	return strings.TrimSuffix(s.Callback, "/api/v1/auth/github/callback") + "/api/v1/auth/" + provider + "/callback"
}
func (s *Service) Save(ctx context.Context, access Access, input Input) (uuid.UUID, error) {
	if input.Provider == "" {
		input.Provider = "github"
	}
	instance := identity.GitHubInstance
	if input.Provider == "gitee" {
		instance = identity.GiteeInstance
	}
	if !identity.ValidProviderInstance(input.Provider, instance) {
		return uuid.Nil, ErrConfig
	}
	if !identity.ValidGitHubSubject(input.BootstrapSubject) {
		return uuid.Nil, ErrConfig
	}
	var validationErr error
	if input.Provider == "gitee" {
		_, validationErr = identity.NewGitee(input.ClientID, input.ClientSecret, s.CallbackFor("gitee"))
	} else {
		_, validationErr = identity.NewGitHub(input.ClientID, input.ClientSecret, s.Callback)
	}
	if validationErr != nil {
		return uuid.Nil, ErrConfig
	}
	id := uuid.New()
	config := Config{Info: Info{ID: id, Provider: input.Provider, Instance: instance, ClientID: input.ClientID, BootstrapSubject: input.BootstrapSubject}, ExpectedActive: input.ExpectedActive}
	plain := []byte(input.ClientSecret)
	defer clear(plain)
	encrypted, err := s.cipher.Seal(plain, configAAD(config))
	if err != nil {
		return uuid.Nil, err
	}
	config.Encrypted = encrypted
	if err := s.Repo.SaveLoginCandidate(ctx, access, config); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
func (s *Service) provider(config Config) (identity.OAuthProvider, error) {
	if !identity.ValidProviderInstance(config.Provider, config.Instance) {
		return nil, ErrConfig
	}
	plain, err := s.cipher.Open(config.Encrypted, configAAD(config))
	if err != nil {
		return nil, ErrConfig
	}
	defer clear(plain)
	if config.Provider == "gitee" {
		return s.GiteeFactory(config.ClientID, string(plain), s.CallbackFor("gitee"))
	}
	return s.Factory(config.ClientID, string(plain), s.Callback)
}
func (s *Service) Start(ctx context.Context, state, nonce, linkToken string, candidate uuid.UUID, access Access) (identity.OAuthProvider, error) {
	return s.StartFor(ctx, state, nonce, linkToken, candidate, access, "")
}
func (s *Service) StartFor(ctx context.Context, state, nonce, linkToken string, candidate uuid.UUID, access Access, provider string) (identity.OAuthProvider, error) {
	config, err := s.Repo.BeginManagedOAuth(ctx, state, nonce, linkToken, candidate, access)
	if err != nil {
		return nil, err
	}
	if provider != "" && provider != config.Provider {
		return nil, identity.ErrOAuthFlow
	}
	return s.provider(config)
}
func (s *Service) Provider(ctx context.Context, ticket string) (identity.OAuthProvider, error) {
	return s.ProviderFor(ctx, ticket, "")
}
func (s *Service) ProviderFor(ctx context.Context, ticket, provider string) (identity.OAuthProvider, error) {
	config, err := s.Repo.ManagedFlowConfig(ctx, ticket)
	if err != nil {
		return nil, err
	}
	if provider != "" && provider != config.Provider {
		return nil, identity.ErrOAuthFlow
	}
	return s.provider(config)
}
