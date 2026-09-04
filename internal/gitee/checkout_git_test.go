package gitee

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/secrets"
)

// This exercises real Git packet negotiation and pack checkout, not mocked
// upload-pack bytes. It is still a fake-provider test, not real Gitee acceptance.
func TestCheckoutBrokerRealGitProtocol(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git required for protocol fixture")
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), git, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture failed: %s", out)
		}
		return strings.TrimSpace(string(out))
	}
	root := t.TempDir()
	work := t.TempDir()
	run(work, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(work, "fixture.txt"), []byte("immutable source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "fixture.txt")
	run(work, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "fixture")
	sha := run(work, "rev-parse", "HEAD")
	run(root, "clone", "--bare", work, filepath.Join(root, "repo.git"))
	upstream := httptest.NewServer(&cgi.Handler{Path: git, Args: []string{"http-backend"}, Dir: root, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=fixture"}})
	defer upstream.Close()
	cipher, _ := secrets.NewCipher(make([]byte, 32))
	store := &checkoutStore{grant: Grant{ID: uuid.New(), Revision: uuid.New(), Subject: "1", Status: "active"}, binding: Binding{ProjectID: uuid.New(), Repository: Repository{ID: "42", Owner: "owner", Name: "repo", AccountID: "7"}}}
	service := New(store, cipher, "https://ci.test")
	service.Provider = checkoutProvider{repo: store.binding.Repository}
	if err := service.seal(&store.grant, Token{Access: "fixture-oauth", Refresh: "fixture-refresh", Scope: "user_info projects", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	broker := NewCheckoutBroker(service, store)
	broker.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		target, _ := url.Parse(upstream.URL + strings.TrimPrefix(r.URL.Path, "/owner") + "?" + r.URL.RawQuery)
		copy.URL = target
		copy.Host = target.Host
		return http.DefaultTransport.RoundTrip(copy)
	})
	proxy := httptest.NewServer(broker)
	defer proxy.Close()
	assignment := &runmodel.Assignment{JobID: uuid.New(), LeaseToken: "fixture-lease", Source: &runmodel.SourceCheckout{Provider: "gitee", RepositoryID: "42", RepositoryUUID: store.binding.ProjectID, CommitSHA: sha}}
	credential, err := broker.IssueAssignmentCredential(t.Context(), uuid.New(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(credential.Token)
	dest := t.TempDir()
	run(dest, "init")
	cmd := exec.CommandContext(t.Context(), git, "-c", "protocol.version=0", "--config-env=http.extraHeader=YUANCI_TEST_GIT_HEADER", "fetch", "--no-tags", "--depth=1", proxy.URL+"/api/v1/checkout/gitee/42.git", sha)
	cmd.Dir = dest
	cmd.Env = append(os.Environ(), "YUANCI_TEST_GIT_HEADER=Authorization: Bearer "+string(credential.Token), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("broker fetch failed: %s", out)
	}
	run(dest, "checkout", "--detach", "FETCH_HEAD")
	if run(dest, "rev-parse", "HEAD") != sha {
		t.Fatal("checkout SHA mismatch")
	}
	data, err := os.ReadFile(filepath.Join(dest, "fixture.txt"))
	if err != nil || string(data) != "immutable source\n" {
		t.Fatal("checkout content mismatch")
	}
}
