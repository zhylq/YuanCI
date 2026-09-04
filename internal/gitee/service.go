package gitee

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

type Grant struct {
	ID        uuid.UUID        `json:"id"`
	UserID    uuid.UUID        `json:"-"`
	LoginID   uuid.UUID        `json:"-"`
	Revision  uuid.UUID        `json:"-"`
	Subject   string           `json:"-"`
	Scope     string           `json:"scope"`
	ExpiresAt time.Time        `json:"expires_at"`
	Status    string           `json:"status"`
	Encrypted secrets.Envelope `json:"-"`
	Claim     uuid.UUID        `json:"-"`
}
type Snapshot struct {
	Config   provisioning.Config
	UserID   uuid.UUID
	Subjects []string
	Grant    *Grant
	FlowID   uuid.UUID
}
type Store interface {
	GiteeContext(context.Context, string, bool) (Snapshot, error)
	BeginGiteeFlow(context.Context, string, Snapshot, string, string) error
	ConsumeGiteeFlow(context.Context, string, string, string) (Snapshot, error)
	SaveGiteeGrant(context.Context, string, Snapshot, Grant) error
	GiteeGrant(context.Context, uuid.UUID) (Grant, provisioning.Config, error)
	ClaimGiteeRefresh(context.Context, Grant) (uuid.UUID, error)
	CompleteGiteeRefresh(context.Context, Grant, Grant, time.Duration, bool) error
	RevokeGiteeGrant(context.Context, string) error
}
type Service struct {
	hooks    hookLimiter
	Store    Store
	Provider OAuthProvider
	cipher   *secrets.Cipher
	Origin   string
}

func New(store Store, cipher *secrets.Cipher, origin string) *Service {
	return &Service{Store: store, Provider: NewClient(), cipher: cipher, Origin: origin}
}

// Reuse the registered login callback; Gitee applications need only one URI.
// Repository authorization still has its own session-bound flow and cookie.
func (s *Service) CallbackURL() string { return s.Origin + "/api/v1/auth/gitee/callback" }
func (s *Service) Start(ctx context.Context, session, state, nonce string) (string, error) {
	snap, err := s.Store.GiteeContext(ctx, session, true)
	if err != nil {
		return "", err
	}
	if err := s.Store.BeginGiteeFlow(ctx, session, snap, state, nonce); err != nil {
		return "", err
	}
	return AuthorizationURL(OAuthConfig{ClientID: snap.Config.ClientID, Callback: s.CallbackURL()}, state), nil
}
func (s *Service) oauth(config provisioning.Config) (OAuthConfig, error) {
	if config.Provider != "gitee" || config.Instance != identity.GiteeInstance {
		return OAuthConfig{}, ErrStale
	}
	plain, err := s.cipher.Open(config.Encrypted, []byte("yuanci:login:gitee:"+identity.GiteeInstance+":"+config.ID.String()))
	if err != nil {
		return OAuthConfig{}, ErrStale
	}
	defer clear(plain)
	return OAuthConfig{ClientID: config.ClientID, Secret: string(plain), Callback: s.CallbackURL()}, nil
}
func GrantAAD(g Grant) []byte {
	return []byte("yuanci:gitee:grant:" + g.ID.String() + ":" + g.UserID.String() + ":" + g.LoginID.String() + ":" + g.Revision.String())
}
func (s *Service) seal(g *Grant, token Token) error {
	if !validToken(token.Access) || !validToken(token.Refresh) || !ValidScope(token.Scope) || !token.ExpiresAt.After(time.Now()) || token.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		return ErrStale
	}
	plain, err := json.Marshal(token)
	if err != nil {
		return ErrStale
	}
	defer clear(plain)
	g.Scope, g.ExpiresAt = token.Scope, token.ExpiresAt
	g.Encrypted, err = s.cipher.Seal(plain, GrantAAD(*g))
	return err
}
func (s *Service) Finish(ctx context.Context, session, state, nonce, code string) error {
	snap, err := s.Store.ConsumeGiteeFlow(ctx, session, state, nonce)
	if err != nil {
		return err
	}
	config, err := s.oauth(snap.Config)
	if err != nil {
		return err
	}
	token, err := s.Provider.Exchange(ctx, config, code)
	if err != nil {
		return err
	}
	user, err := s.Provider.User(ctx, token.Access)
	if err != nil {
		return err
	}
	if user.Provider != "gitee" || user.Instance != identity.GiteeInstance || !slices.Contains(snap.Subjects, user.Subject) {
		return scm.ErrUnauthorized
	}
	grant := Grant{ID: uuid.New(), UserID: snap.UserID, LoginID: snap.Config.ID, Revision: uuid.New(), Subject: user.Subject, Status: "active"}
	if snap.Grant != nil {
		grant.ID = snap.Grant.ID
	}
	if err := s.seal(&grant, token); err != nil {
		return err
	}
	return s.Store.SaveGiteeGrant(ctx, session, snap, grant)
}

// Access returns control-plane material only. OAuth grants are not repository-
// scoped installation tokens and must never be sent to a Runner as one.
func (s *Service) Access(ctx context.Context, id uuid.UUID) ([]byte, error) {
	grant, config, err := s.Store.GiteeGrant(ctx, id)
	if err != nil {
		return nil, err
	}
	plain, err := s.cipher.Open(grant.Encrypted, GrantAAD(grant))
	if err != nil {
		return nil, ErrStale
	}
	defer clear(plain)
	var token Token
	if json.Unmarshal(plain, &token) != nil || !ValidScope(token.Scope) || !validToken(token.Access) {
		return nil, ErrStale
	}
	if grant.Status == "active" && grant.ExpiresAt.After(time.Now().Add(2*time.Minute)) {
		return []byte(token.Access), nil
	}
	claim, err := s.Store.ClaimGiteeRefresh(ctx, grant)
	if err != nil {
		return nil, err
	}
	grant.Claim = claim
	oauth, err := s.oauth(config)
	if err == nil {
		token, err = s.Provider.Refresh(ctx, oauth, token.Refresh)
	}
	if err != nil {
		delay := time.Duration(0)
		var rate RateError
		if errors.As(err, &rate) {
			delay = rate.After
		}
		// Ambiguous exchange failure may have consumed the refresh token. Require
		// reauthorization instead of replaying a possibly rotated credential.
		if finishErr := s.Store.CompleteGiteeRefresh(ctx, grant, Grant{}, delay, delay == 0); finishErr != nil {
			return nil, finishErr
		}
		return nil, err
	}
	updated := grant
	updated.Revision = uuid.New()
	updated.Claim = uuid.Nil
	updated.Status = "active"
	if err := s.seal(&updated, token); err != nil {
		_ = s.Store.CompleteGiteeRefresh(ctx, grant, Grant{}, 0, true)
		return nil, err
	}
	if err := s.Store.CompleteGiteeRefresh(ctx, grant, updated, 0, false); err != nil {
		return nil, err
	}
	return []byte(token.Access), nil
}
