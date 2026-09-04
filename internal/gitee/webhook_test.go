package gitee

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yuanci/yuanci/internal/scm"
)

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
