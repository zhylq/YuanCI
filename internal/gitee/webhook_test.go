package gitee

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yuanci/yuanci/internal/scm"
)

func TestVerifyPushUsesImmutableCommitWhenBranchHasAdvanced(t *testing.T) {
	oldSHA, currentSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	repo := Repository{ID: "42", Owner: "owner", Name: "repo"}
	c := NewClient()
	lookedUp := ""
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v5/repos/owner/repo":
			return response(r, 200, `{"id":42,"path":"repo","namespace":{"id":7,"path":"owner"},"default_branch":"main","html_url":"https://gitee.com/owner/repo","permission":{"admin":true}}`), nil
		case "/api/v5/repos/owner/repo/commits/" + oldSHA:
			lookedUp = oldSHA
			return response(r, 200, `{"sha":"`+oldSHA+`"}`), nil
		default:
			lookedUp = r.URL.Path
			return response(r, 200, `{"sha":"`+currentSHA+`"}`), nil
		}
	})
	event := scm.Event{Type: scm.EventPush, Repository: scm.Repository{ExternalID: "42"}, Ref: "refs/heads/main", AfterSHA: oldSHA}
	if err := c.VerifyEvent(t.Context(), "access", repo, event); err != nil {
		t.Fatalf("delayed push rejected after branch advanced: %v", err)
	}
	if lookedUp != oldSHA {
		t.Fatalf("event verification used mutable branch: %q", lookedUp)
	}
}

func TestVerifyPushRejectsMalformedEventAndRepositoryOrCommitMismatch(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, test := range []struct {
		name                                            string
		typ                                             scm.EventType
		ref, after, eventRepo, currentRepo, returnedSHA string
	}{
		{"malformed ref", scm.EventPush, "refs/heads/main//next", sha, "42", "42", sha},
		{"tag disguised as push", scm.EventPush, "refs/tags/main", sha, "42", "42", sha},
		{"branch disguised as tag", scm.EventTag, "refs/heads/main", sha, "42", "42", sha},
		{"unsupported type", scm.EventType("deploy"), "refs/heads/main", sha, "42", "42", sha},
		{"bad sha", scm.EventPush, "refs/heads/main", "main", "42", "42", sha},
		{"different event repo", scm.EventPush, "refs/heads/main", sha, "43", "42", sha},
		{"replaced repository", scm.EventPush, "refs/heads/main", sha, "42", "43", sha},
		{"different commit", scm.EventPush, "refs/heads/main", sha, "42", "42", strings.Repeat("b", 40)},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := NewClient()
			c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v5/repos/owner/repo" {
					return response(r, 200, fmt.Sprintf(`{"id":%s,"path":"repo","namespace":{"id":7,"path":"owner"},"default_branch":"main","html_url":"https://gitee.com/owner/repo","permission":{"admin":true}}`, test.currentRepo)), nil
				}
				return response(r, 200, `{"sha":"`+test.returnedSHA+`"}`), nil
			})
			err := c.VerifyEvent(t.Context(), "access", Repository{ID: "42", Owner: "owner", Name: "repo"}, scm.Event{Type: test.typ, Ref: test.ref, AfterSHA: test.after, Repository: scm.Repository{ExternalID: test.eventRepo}})
			if !errors.Is(err, scm.ErrInvalidHook) {
				t.Fatalf("invalid event was not rejected: %v", err)
			}
		})
	}
}

func TestVerifyDelayedPushRequiresCommitInBoundRepository(t *testing.T) {
	c := NewClient()
	sha := strings.Repeat("a", 40)
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v5/repos/owner/repo" {
			return response(r, 200, `{"id":42,"path":"repo","namespace":{"id":7,"path":"owner"},"default_branch":"main","html_url":"https://gitee.com/owner/repo","permission":{"admin":true}}`), nil
		}
		if r.URL.Path != "/api/v5/repos/owner/repo/commits/"+sha {
			t.Fatalf("lookup escaped immutable repository scope: %s", r.URL.Path)
		}
		return response(r, 404, `{}`), nil
	})
	err := c.VerifyEvent(t.Context(), "access", Repository{ID: "42", Owner: "owner", Name: "repo"}, scm.Event{Type: scm.EventPush, Ref: "refs/heads/main", AfterSHA: sha, Repository: scm.Repository{ExternalID: "42"}})
	if !errors.Is(err, scm.ErrNotFound) {
		t.Fatalf("nonexistent commit accepted: %v", err)
	}
}

func TestVerifyPullRequestRetainsCurrentTrustAndIdentityChecks(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, mode := range []string{"valid", "closed", "changed SHA", "changed head", "changed base", "external fork", "changed base repository", "changed number", "untrusted event", "missing number"} {
		t.Run(mode, func(t *testing.T) {
			pr := pullRequest{Number: 1, State: "open"}
			pr.Head.SHA, pr.Head.Ref, pr.Head.Repo.ID = sha, "feature", 42
			pr.Base.Ref, pr.Base.Repo.ID = "main", 42
			event := scm.Event{Type: scm.EventPullRequest, Ref: "refs/heads/feature", AfterSHA: sha, Repository: scm.Repository{ExternalID: "42"}, Metadata: map[string]string{"fork": "false", "base_ref": "main", "pull_request_number": "1"}}
			switch mode {
			case "closed":
				pr.State = "closed"
			case "changed SHA":
				pr.Head.SHA = strings.Repeat("b", 40)
			case "changed head":
				pr.Head.Ref = "other"
			case "changed base":
				pr.Base.Ref = "other"
			case "external fork":
				pr.Head.Repo.ID = 43
			case "changed base repository":
				pr.Base.Repo.ID = 43
			case "changed number":
				pr.Number = 2
			case "untrusted event":
				event.Metadata["fork"] = "true"
			case "missing number":
				delete(event.Metadata, "pull_request_number")
			}
			body, err := json.Marshal(pr)
			if err != nil {
				t.Fatal(err)
			}
			c := NewClient()
			c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v5/repos/owner/repo" {
					return response(r, 200, `{"id":42,"path":"repo","namespace":{"id":7,"path":"owner"},"default_branch":"main","html_url":"https://gitee.com/owner/repo","permission":{"admin":true}}`), nil
				}
				if r.URL.Path != "/api/v5/repos/owner/repo/pulls/1" {
					t.Fatalf("PR verification lost its identity check: %s", r.URL.Path)
				}
				return response(r, 200, string(body)), nil
			})
			err = c.VerifyEvent(t.Context(), "access", Repository{ID: "42", Owner: "owner", Name: "repo"}, event)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			want := scm.ErrInvalidHook
			if mode == "untrusted event" {
				want = scm.ErrUnauthorized
			}
			if !errors.Is(err, want) {
				t.Fatalf("untrusted PR accepted: %v", err)
			}
		})
	}
}

func TestWebhookPasswordBindingReplayAndFork(t *testing.T) {
	now := time.Now()
	secret := []byte(strings.Repeat("s", 32))
	repo := Repository{ID: "42", Owner: "owner", Name: "repo"}
	headers := http.Header{"X-Gitee-Token": {string(secret)}, "X-Gitee-Timestamp": {fmt.Sprint(now.UnixMilli())}, "X-Gitee-Event": {"Push Hook"}}
	body := []byte(`{"ref":"refs/heads/main","before":"` + strings.Repeat("a", 40) + `","after":"` + strings.Repeat("b", 40) + `","repository":{"id":42,"clone_url":"https://attacker.test"}}`)
	event, err := NormalizeWebhook(headers, body, secret, repo, now)
	if err != nil || event.Provider != scm.Gitee || event.Repository.CloneURL != "" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	again, err := NormalizeWebhook(headers, body, secret, repo, now.Add(time.Second))
	if err != nil || again.DeliveryID != event.DeliveryID {
		t.Fatal("unstable delivery identity")
	}
	headers.Set("X-Gitee-Token", "timestamp-signature")
	if _, err := NormalizeWebhook(headers, body, secret, repo, now); err == nil {
		t.Fatal("timestamp-only signature accepted as body authentication")
	}
	headers.Set("X-Gitee-Token", string(secret))
	if _, err := NormalizeWebhook(headers, []byte(strings.Replace(string(body), `"id":42`, `"id":43`, 1)), secret, repo, now); err == nil {
		t.Fatal("repository substituted")
	}
	headers.Set("X-Gitee-Timestamp", fmt.Sprint(now.Add(-2*time.Hour).UnixMilli()))
	if _, err := NormalizeWebhook(headers, body, secret, repo, now); err == nil {
		t.Fatal("stale replay accepted")
	}
	headers.Set("X-Gitee-Timestamp", fmt.Sprint(now.UnixMilli()))
	headers.Set("X-Gitee-Event", "Merge Request Hook")
	pr := []byte(`{"pull_request":{"number":1,"state":"open","head":{"sha":"` + strings.Repeat("b", 40) + `","ref":"feature","repo":{"id":99}},"base":{"ref":"main","repo":{"id":42}}}}`)
	fork, err := NormalizeWebhook(headers, pr, secret, repo, now)
	if err != nil || fork.Metadata["fork"] != "true" {
		t.Fatal("fork not marked")
	}
}
func TestImmutableFileLookupPinsCommitAndBoundsContent(t *testing.T) {
	c := NewClient()
	sha := strings.Repeat("a", 40)
	content := "version: v1\n"
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("ref") != sha || r.URL.Path != "/api/v5/repos/owner/repo/contents/.yuanci.yml" {
			t.Fatal("mutable ref or wrong path")
		}
		return response(r, 200, fmt.Sprintf(`{"type":"file","path":".yuanci.yml","encoding":"base64","size":%d,"content":%q}`, len(content), base64.StdEncoding.EncodeToString([]byte(content)))), nil
	})
	data, err := c.File(t.Context(), "access", Repository{Owner: "owner", Name: "repo"}, ".yuanci.yml", sha)
	if err != nil || string(data) != content {
		t.Fatalf("file: %v", err)
	}
	if _, err := c.File(t.Context(), "access", Repository{Owner: "owner", Name: "repo"}, ".yuanci.yml", "main"); err == nil {
		t.Fatal("mutable ref accepted")
	}
}
