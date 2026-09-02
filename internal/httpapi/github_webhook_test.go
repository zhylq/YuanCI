package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubhook"
)

const httpWebhookSecret = "http-webhook-secret-for-tests-1234"

type httpHookSecret struct{}

func (httpHookSecret) WebhookSecret(context.Context) ([]byte, error) {
	return []byte(httpWebhookSecret), nil
}

type httpHookInbox struct{ count int }

func (s *httpHookInbox) ReceiveWebhook(_ context.Context, _ githubhook.Delivery) (githubhook.Receipt, error) {
	s.count++
	return githubhook.Receipt{ID: uuid.MustParse("00000000-0000-4000-8000-000000000123")}, nil
}

func TestGitHubWebhookHTTPAcceptedAndAuthenticationFailure(t *testing.T) {
	inbox := &httpHookInbox{}
	hooks, err := githubhook.New(httpHookSecret{}, inbox)
	if err != nil {
		t.Fatal(err)
	}
	api := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), githubHooks: hooks}
	body := []byte(`{"ref":"refs/heads/main","before":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":42,"name":"widget","owner":{"login":"acme"}},"sender":{"login":"octocat"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytesReader(body))
	request.Header = httpSignedHeaders(body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.receiveGitHubWebhook(response, request)
	if response.Code != http.StatusAccepted || inbox.count != 1 || !strings.Contains(response.Body.String(), "00000000-0000-4000-8000-000000000123") {
		t.Fatalf("status=%d count=%d body=%s", response.Code, inbox.count, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytesReader(body))
	request.Header = httpSignedHeaders(body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
	response = httptest.NewRecorder()
	api.receiveGitHubWebhook(response, request)
	if response.Code != http.StatusUnauthorized || inbox.count != 1 {
		t.Fatalf("invalid signature status=%d count=%d", response.Code, inbox.count)
	}
}

func bytesReader(body []byte) *strings.Reader { return strings.NewReader(string(body)) }

func httpSignedHeaders(body []byte) http.Header {
	mac := hmac.New(sha256.New, []byte(httpWebhookSecret))
	_, _ = mac.Write(body)
	return http.Header{
		"X-Github-Event":      {"push"},
		"X-Github-Delivery":   {"delivery-http"},
		"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(mac.Sum(nil))},
	}
}
