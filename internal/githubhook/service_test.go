package githubhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testSecret = "webhook-secret-value-for-tests-9876"
const testSHA = "0123456789abcdef0123456789abcdef01234567"

type secretFixture struct{ value []byte }

func (s secretFixture) WebhookSecret(context.Context) ([]byte, error) {
	return append([]byte(nil), s.value...), nil
}

type inboxFixture struct {
	count  int
	last   Delivery
	result Receipt
	err    error
}

func (s *inboxFixture) ReceiveWebhook(_ context.Context, delivery Delivery) (Receipt, error) {
	s.count++
	s.last = delivery
	return s.result, s.err
}

func TestReceiveAuthenticatesNormalizesAndRecords(t *testing.T) {
	inbox := &inboxFixture{result: Receipt{ID: uuid.New()}}
	service, err := New(secretFixture{[]byte(testSecret)}, inbox)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"ref":"refs/heads/main","before":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","after":"` + testSHA + `","repository":{"id":42,"name":"widget","owner":{"login":"acme"},"clone_url":"https://github.com/acme/widget.git","html_url":"https://github.com/acme/widget","default_branch":"main","private":true},"sender":{"login":"octocat"}}`)
	receipt, err := service.Receive(t.Context(), signedHeaders("push", "delivery-1", body), body)
	if err != nil || receipt.ID != inbox.result.ID {
		t.Fatalf("receive: %#v %v", receipt, err)
	}
	if inbox.count != 1 || inbox.last.Provider != "github" || inbox.last.EventType != "push" || len(inbox.last.PayloadSHA256) != 64 {
		t.Fatalf("unexpected delivery: %#v", inbox.last)
	}
	if strings.Contains(string(inbox.last.NormalizedEvent), testSecret) || strings.Contains(string(inbox.last.NormalizedEvent), "private_key") {
		t.Fatal("secret leaked into normalized event")
	}
}

func TestReceiveRejectsBeforeInboxMutation(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	for _, test := range []struct {
		name   string
		mutate func(http.Header)
	}{
		{"bad signature", func(h http.Header) { h.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64)) }},
		{"duplicate signature", func(h http.Header) { h.Add("X-Hub-Signature-256", h.Get("X-Hub-Signature-256")) }},
		{"missing delivery", func(h http.Header) { h.Del("X-GitHub-Delivery") }},
		{"ambiguous event", func(h http.Header) { h.Add("X-GitHub-Event", "push") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			inbox := &inboxFixture{}
			service, _ := New(secretFixture{[]byte(testSecret)}, inbox)
			headers := signedHeaders("push", "delivery-1", body)
			test.mutate(headers)
			if _, err := service.Receive(t.Context(), headers, body); err == nil {
				t.Fatal("invalid request accepted")
			}
			if inbox.count != 0 {
				t.Fatal("inbox mutated before authentication and validation")
			}
		})
	}
}

func TestReceiveClassifiesExternalFork(t *testing.T) {
	inbox := &inboxFixture{result: Receipt{ID: uuid.New()}}
	service, _ := New(secretFixture{[]byte(testSecret)}, inbox)
	body := []byte(`{"action":"opened","number":7,"repository":{"id":42,"name":"widget","owner":{"login":"acme"}},"sender":{"login":"octocat"},"pull_request":{"head":{"ref":"feature","sha":"` + testSHA + `","repo":{"id":99,"name":"widget","owner":{"login":"forker"}}},"base":{"ref":"main"}}}`)
	if _, err := service.Receive(t.Context(), signedHeaders("pull_request", "delivery-pr", body), body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inbox.last.NormalizedEvent), `"fork":"true"`) {
		t.Fatalf("fork not classified: %s", inbox.last.NormalizedEvent)
	}
}

func TestReceivePropagatesConflict(t *testing.T) {
	inbox := &inboxFixture{err: ErrConflict}
	service, _ := New(secretFixture{[]byte(testSecret)}, inbox)
	body := []byte(`{"ref":"refs/heads/main","after":"` + testSHA + `","repository":{"id":42,"name":"widget","owner":{"login":"acme"}},"sender":{"login":"octocat"}}`)
	_, err := service.Receive(t.Context(), signedHeaders("push", "delivery-1", body), body)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestReceiveRateLimitIsBounded(t *testing.T) {
	inbox := &inboxFixture{}
	service, _ := New(secretFixture{[]byte(testSecret)}, inbox)
	fixed := time.Now()
	service.now = func() time.Time { return fixed }
	body := []byte(`{}`)
	headers := signedHeaders("push", "delivery-1", body)
	for range 100 {
		_, _ = service.Receive(t.Context(), headers, body)
	}
	if _, err := service.Receive(t.Context(), headers, body); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
}

func signedHeaders(event, delivery string, body []byte) http.Header {
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = mac.Write(body)
	return http.Header{"X-Github-Event": {event}, "X-Github-Delivery": {delivery}, "X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(mac.Sum(nil))}}
}
