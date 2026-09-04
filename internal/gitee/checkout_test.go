package gitee

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/secrets"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type checkoutStore struct {
	Store
	AutomationStore
	grant   Grant
	binding Binding
	denied  atomic.Bool
}

func (s *checkoutStore) GiteeGrant(context.Context, uuid.UUID) (Grant, provisioning.Config, error) {
	return s.grant, provisioning.Config{}, nil
}
func (s *checkoutStore) ResolveGiteeRepository(context.Context, string) (Binding, error) {
	return s.binding, nil
}
func (s *checkoutStore) CheckGiteeCheckoutLease(context.Context, runmodel.LeaseRequest, uuid.UUID, string) error {
	if s.denied.Load() {
		return runmodel.ErrLeaseInvalid
	}
	return nil
}

type checkoutProvider struct {
	OAuthProvider
	RepositoryProvider
	repo Repository
}

func (p checkoutProvider) User(context.Context, string) (identity.ExternalUser, error) {
	return identity.ExternalUser{Subject: "1", Login: "owner"}, nil
}
func (p checkoutProvider) Repository(context.Context, string, string, string) (Repository, error) {
	return p.repo, nil
}

func TestCheckoutBrokerScopeExpiryAndRevocation(t *testing.T) {
	cipher, _ := secrets.NewCipher(make([]byte, 32))
	store := &checkoutStore{grant: Grant{ID: uuid.New(), Revision: uuid.New(), Subject: "1", Status: "active"}, binding: Binding{ProjectID: uuid.New(), Repository: Repository{ID: "42", Owner: "owner", Name: "repo", AccountID: "7"}}}
	service := New(store, cipher, "https://ci.test")
	service.Provider = checkoutProvider{repo: store.binding.Repository}
	if err := service.seal(&store.grant, Token{Access: "broad-oauth-secret", Refresh: "refresh", Scope: "user_info projects", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	broker := NewCheckoutBroker(service, store)
	a := &runmodel.Assignment{JobID: uuid.New(), LeaseToken: "job-lease", Source: &runmodel.SourceCheckout{Provider: "gitee", RepositoryID: "42", RepositoryUUID: store.binding.ProjectID, CommitSHA: strings.Repeat("a", 40)}}
	credential, err := broker.IssueAssignmentCredential(t.Context(), uuid.New(), a)
	if err != nil || strings.Contains(string(credential.Token), "oauth") {
		t.Fatal("issuance", err)
	}
	defer clear(credential.Token)
	calls := 0
	broker.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		user, password, ok := r.BasicAuth()
		if !ok || user != "owner" || password != "broad-oauth-secret" || r.URL.Host != "gitee.com" || r.URL.Path != "/owner/repo.git/info/refs" {
			t.Fatal("upstream trust")
		}
		reply := response(r, 200, "safe-advertisement")
		reply.Header.Set("Content-Type", "application/x-git-upload-pack-advertisement")
		reply.Header.Set("Set-Cookie", "private")
		return reply, nil
	})
	get := func(path, token string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		broker.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "secret") || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("leak")
		}
		return w.Code
	}
	good := credential.CloneURL + "/info/refs?service=git-upload-pack"
	if get(good, string(credential.Token)) != 200 || calls != 1 {
		t.Fatal("valid checkout")
	}
	if get(strings.Replace(good, "42.git", "43.git", 1), string(credential.Token)) != 403 || get(good, "broad-oauth-secret") != 403 || calls != 1 {
		t.Fatal("cross-repository token")
	}
	store.denied.Store(true)
	if get(good, string(credential.Token)) != 403 || calls != 1 {
		t.Fatal("lease revocation")
	}
	store.denied.Store(false)
	broker.entries[sha256.Sum256(credential.Token)].expires = time.Now().Add(-time.Second)
	if get(good, string(credential.Token)) != 403 || calls != 1 {
		t.Fatal("expired token")
	}
}

func TestCheckoutUploadOnlyAssignedCommit(t *testing.T) {
	sha := strings.Repeat("a", 40)
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	valid := pkt("want "+sha+" multi_ack_detailed side-band-64k thin-pack ofs-delta agent=git/2.47.2\n") + pkt("deepen 1\n") + "0000" + pkt("done\n")
	if !validUpload([]byte(valid), sha) {
		t.Fatal("valid shallow fetch rejected")
	}
	for _, body := range []string{strings.Replace(valid, sha, strings.Repeat("b", 40), 1), pkt("want "+sha+"\n") + pkt("deepen 2\n"), pkt("command=fetch\n"), "ffffshort", valid + pkt("want "+sha+"\n"), pkt("want "+sha+"\n") + pkt("done\n"), pkt("want "+sha+" filter\n") + pkt("deepen 1\n")} {
		if validUpload([]byte(body), sha) {
			t.Fatal("unsafe fetch accepted")
		}
	}
}
