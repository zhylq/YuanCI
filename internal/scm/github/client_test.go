package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuanci/yuanci/internal/scm"
)

const testSecret = "github-webhook-secret-for-tests"

func TestAuthenticatedUserAndRepositoryDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-GitHub-Api-Version") != apiVersion {
			t.Errorf("unexpected API version %q", r.Header.Get("X-GitHub-Api-Version"))
		}
		switch r.URL.Path {
		case "/user":
			_, _ = io.WriteString(w, `{"login":"octocat"}`)
		case "/user/repos":
			if r.URL.Query().Get("per_page") != "100" {
				t.Errorf("unexpected pagination: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `[{"id":42,"name":"hello","owner":{"login":"octocat"},"clone_url":"https://github.com/octocat/hello.git","html_url":"https://github.com/octocat/hello","default_branch":"main","private":true}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newClient(server.URL, "token", []byte(testSecret), server.Client())

	user, err := client.CurrentUser(t.Context())
	if err != nil || user != "octocat" {
		t.Fatalf("CurrentUser() = %q, %v", user, err)
	}
	repositories, err := client.ListRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].ExternalID != "42" || !repositories[0].Private {
		t.Fatalf("unexpected repositories: %#v", repositories)
	}
}

func TestGetFileUsesRawMediaTypeAndRejectsTraversal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/repos/acme/widget/contents/ci/pipeline.yml" {
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
		}
		if r.URL.Query().Get("ref") != "main" || r.Header.Get("Accept") != "application/vnd.github.raw+json" {
			t.Errorf("unexpected request: query=%s accept=%s", r.URL.RawQuery, r.Header.Get("Accept"))
		}
		_, _ = io.WriteString(w, "version: v1\n")
	}))
	defer server.Close()
	client := newClient(server.URL, "token", []byte(testSecret), server.Client())
	repository := scm.Repository{Owner: "acme", Name: "widget"}

	body, err := client.GetFile(t.Context(), repository, "ci/pipeline.yml", "main")
	if err != nil || string(body) != "version: v1\n" {
		t.Fatalf("GetFile() = %q, %v", body, err)
	}
	if _, err := client.GetFile(t.Context(), repository, "../secret", "main"); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}

func TestWebhookAndCommitStatusWrites(t *testing.T) {
	var webhookPayload, statusPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widget/hooks":
			if err := json.NewDecoder(r.Body).Decode(&webhookPayload); err != nil {
				t.Fatal(err)
			}
		case "/repos/acme/widget/statuses/abc123":
			if err := json.NewDecoder(r.Body).Decode(&statusPayload); err != nil {
				t.Fatal(err)
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client := newClient(server.URL, "token", []byte(testSecret), server.Client())
	repository := scm.Repository{Owner: "acme", Name: "widget"}

	err := client.CreateWebhook(t.Context(), repository, "https://ci.example.test/webhooks/github", []scm.EventType{
		scm.EventPush, scm.EventTag, scm.EventPullRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := webhookPayload["events"].([]any)
	if len(events) != 2 || events[0] != "push" || events[1] != "pull_request" {
		t.Fatalf("webhook events were not de-duplicated: %#v", events)
	}
	config := webhookPayload["config"].(map[string]any)
	if config["insecure_ssl"] != "0" || config["secret"] != testSecret {
		t.Fatalf("unsafe webhook config: %#v", config)
	}

	err = client.SetCommitStatus(t.Context(), repository, scm.CommitStatus{
		SHA: "abc123", Context: "yuanci/verify", State: "success",
		Description: "passed", TargetURL: "https://ci.example.test/runs/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if statusPayload["state"] != "success" || statusPayload["context"] != "yuanci/verify" {
		t.Fatalf("unexpected status payload: %#v", statusPayload)
	}
}

func TestCreatePipelineChange(t *testing.T) {
	requests := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/widget/git/ref/heads/main":
			_, _ = io.WriteString(w, `{"object":{"sha":"base-sha"}}`)
		case "POST /repos/acme/widget/git/refs":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		case "PUT /repos/acme/widget/contents/.yuanci.yml":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		case "POST /repos/acme/widget/pulls":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"number":7,"title":"Add CI","html_url":"https://github.test/acme/widget/pull/7","head":{"ref":"yuanci/setup","sha":"head-sha"},"base":{"ref":"main"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newClient(server.URL, "token", []byte(testSecret), server.Client())
	repository := scm.Repository{Owner: "acme", Name: "widget", DefaultBranch: "main"}

	pullRequest, err := client.CreatePipelineChange(t.Context(), repository, "yuanci/setup", "Add CI", strings.NewReader("version: v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if pullRequest.Number != 7 || pullRequest.SHA != "head-sha" || len(requests) != 4 {
		t.Fatalf("unexpected pull request or calls: %#v %#v", pullRequest, requests)
	}
}

func TestParsePushTagAndPullRequestWebhooks(t *testing.T) {
	client := New("", []byte(testSecret))
	fixedNow := time.Date(2026, 8, 26, 9, 30, 0, 0, time.UTC)
	client.now = func() time.Time { return fixedNow }
	repository := `"repository":{"id":42,"name":"widget","owner":{"login":"acme"},"clone_url":"https://github.com/acme/widget.git","html_url":"https://github.com/acme/widget","default_branch":"main","private":false}`

	pushBody := []byte(`{"ref":"refs/tags/v1.0.0","before":"old","after":"new",` + repository + `,"sender":{"login":"octocat"}}`)
	tagEvent, err := client.ParseWebhook(signedHeaders("push", "delivery-tag", pushBody), pushBody)
	if err != nil {
		t.Fatal(err)
	}
	if tagEvent.Type != scm.EventTag || tagEvent.AfterSHA != "new" || tagEvent.Repository.ExternalID != "42" || !tagEvent.ReceivedAt.Equal(fixedNow) {
		t.Fatalf("unexpected tag event: %#v", tagEvent)
	}

	pullBody := []byte(`{"action":"opened","number":7,` + repository + `,"sender":{"login":"octocat"},"pull_request":{"head":{"ref":"feature","sha":"pr-sha"},"base":{"ref":"main"}}}`)
	pullEvent, err := client.ParseWebhook(signedHeaders("pull_request", "delivery-pr", pullBody), pullBody)
	if err != nil {
		t.Fatal(err)
	}
	if pullEvent.Type != scm.EventPullRequest || pullEvent.Ref != "refs/pull/7/head" || pullEvent.Metadata["base_ref"] != "main" {
		t.Fatalf("unexpected pull request event: %#v", pullEvent)
	}

	tampered := append([]byte(nil), pushBody...)
	tampered[0] = '['
	if _, err := client.ParseWebhook(signedHeaders("push", "delivery-tag", pushBody), tampered); !errors.Is(err, scm.ErrInvalidHook) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
}

func TestRateLimitClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	client := newClient(server.URL, "token", []byte(testSecret), server.Client())
	if _, err := client.CurrentUser(context.Background()); !errors.Is(err, scm.ErrRateLimited) {
		t.Fatalf("expected rate limit error, got %v", err)
	}
}

func signedHeaders(event, delivery string, body []byte) map[string][]string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = mac.Write(body)
	return map[string][]string{
		"x-github-event":      {event},
		"X-GitHub-Delivery":   {delivery},
		"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(mac.Sum(nil))},
	}
}
