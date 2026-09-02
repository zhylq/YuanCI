// Package githubhook authenticates and durably records GitHub App webhooks.
package githubhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/scm"
	scmgithub "github.com/yuanci/yuanci/internal/scm/github"
	"github.com/yuanci/yuanci/internal/scm/webhook"
)

const MaxBodyBytes = 2 << 20

var (
	ErrInvalidRequest = errors.New("invalid GitHub webhook request")
	ErrConflict       = errors.New("GitHub delivery ID conflicts with an existing payload")
	ErrRateLimited    = errors.New("GitHub webhook rate limit reached")
	ErrNoDelivery     = errors.New("no GitHub webhook delivery is ready")
	ErrLeaseInvalid   = errors.New("GitHub webhook lease is invalid or expired")
	deliveryPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)
	shaPattern        = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

const MaxAttempts = 12

type SecretSource interface {
	WebhookSecret(context.Context) ([]byte, error)
}

type Store interface {
	ReceiveWebhook(context.Context, Delivery) (Receipt, error)
}

type Delivery struct {
	Provider         string
	ProviderInstance string
	DeliveryID       string
	EventType        string
	PayloadSHA256    string
	NormalizedEvent  json.RawMessage
	ReceivedAt       time.Time
}

type Receipt struct {
	ID        uuid.UUID `json:"id"`
	Duplicate bool      `json:"duplicate"`
}

type WorkItem struct {
	ID           uuid.UUID
	LeaseID      uuid.UUID
	Event        scm.Event
	Attempt      int
	LeaseExpires time.Time
}

type FinalState string

const (
	FinalProcessed FinalState = "processed"
	FinalIgnored   FinalState = "ignored"
	FinalRetry     FinalState = "retry"
	FinalDead      FinalState = "dead_letter"
)

type Finalize struct {
	ID           uuid.UUID
	LeaseID      uuid.UUID
	State        FinalState
	NextAttempt  time.Time
	ErrorCode    string
	ErrorSummary string
}

type WorkStore interface {
	ClaimWebhook(context.Context, time.Duration) (*WorkItem, error)
	FinalizeWebhook(context.Context, Finalize) error
	RecoverWebhookLeases(context.Context, int) (int, error)
}

type Service struct {
	secrets SecretSource
	store   Store
	mu      sync.Mutex
	tokens  float64
	last    time.Time
	now     func() time.Time
}

func New(secrets SecretSource, store Store) (*Service, error) {
	if secrets == nil || store == nil {
		return nil, errors.New("GitHub webhook service requires a secret source and inbox")
	}
	now := time.Now()
	return &Service{secrets: secrets, store: store, tokens: 100, last: now, now: time.Now}, nil
}

func (s *Service) Receive(ctx context.Context, headers http.Header, body []byte) (Receipt, error) {
	if !s.allow() {
		return Receipt{}, ErrRateLimited
	}
	if len(body) == 0 || len(body) > MaxBodyBytes {
		return Receipt{}, ErrInvalidRequest
	}
	deliveryID, ok := singleHeader(headers, "X-GitHub-Delivery")
	if !ok || !deliveryPattern.MatchString(deliveryID) {
		return Receipt{}, ErrInvalidRequest
	}
	eventType, ok := singleHeader(headers, "X-GitHub-Event")
	if !ok || len(eventType) > 64 {
		return Receipt{}, ErrInvalidRequest
	}
	signature, ok := singleHeader(headers, "X-Hub-Signature-256")
	if !ok {
		return Receipt{}, scm.ErrInvalidHook
	}
	secret, err := s.secrets.WebhookSecret(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer clear(secret)
	if err := webhook.VerifyHMACSHA256(secret, body, signature, "sha256="); err != nil {
		return Receipt{}, scm.ErrInvalidHook
	}
	event, err := scmgithub.New("", secret).ParseWebhook(map[string][]string(headers), body)
	if err != nil {
		if errors.Is(err, scm.ErrInvalidHook) {
			return Receipt{}, ErrInvalidRequest
		}
		return Receipt{}, err
	}
	if err := validateEvent(event); err != nil {
		return Receipt{}, err
	}
	// Never persist remote URLs supplied by a webhook. Later workers resolve a
	// trusted clone URL from the imported repository record.
	event.Repository.CloneURL = ""
	event.Repository.WebURL = ""
	normalized, err := json.Marshal(event)
	if err != nil {
		return Receipt{}, ErrInvalidRequest
	}
	return s.store.ReceiveWebhook(ctx, Delivery{
		Provider: "github", ProviderInstance: identity.GitHubInstance,
		DeliveryID: deliveryID, EventType: eventType, PayloadSHA256: webhook.PayloadDigest(body),
		NormalizedEvent: normalized, ReceivedAt: event.ReceivedAt,
	})
}

// allow implements a process-local 50 requests/second limiter with a burst of
// 100. Database idempotency remains the cross-instance protection.
func (s *Service) allow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	elapsed := now.Sub(s.last).Seconds()
	if elapsed > 0 {
		s.tokens += elapsed * 50
		if s.tokens > 100 {
			s.tokens = 100
		}
		s.last = now
	}
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

func singleHeader(headers http.Header, name string) (string, bool) {
	values := headers.Values(name)
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, value != "" && !strings.ContainsAny(value, "\r\n\x00")
}

func validateEvent(event scm.Event) error {
	if event.Provider != scm.GitHub || !deliveryPattern.MatchString(event.DeliveryID) ||
		event.Repository.ExternalID == "" || event.Repository.Owner == "" || event.Repository.Name == "" ||
		len(event.Repository.Owner) > 100 || len(event.Repository.Name) > 100 ||
		strings.ContainsAny(event.Repository.Owner+event.Repository.Name+event.Ref+event.Sender, "\r\n\x00") ||
		len(event.Ref) > 1024 || len(event.Sender) > 100 || !shaPattern.MatchString(event.AfterSHA) {
		return ErrInvalidRequest
	}
	switch event.Type {
	case scm.EventPush, scm.EventTag, scm.EventPullRequest:
		if event.Type == scm.EventPullRequest && event.Metadata["fork"] != "true" && event.Metadata["fork"] != "false" {
			return ErrInvalidRequest
		}
		return nil
	default:
		return ErrInvalidRequest
	}
}
