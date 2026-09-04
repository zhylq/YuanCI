package gitee

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/identity"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

type CheckoutLeaseStore interface {
	CheckGiteeCheckoutLease(context.Context, runmodel.LeaseRequest, uuid.UUID, string) error
}
type checkoutEntry struct {
	lease   runmodel.LeaseRequest
	source  runmodel.SourceCheckout
	expires time.Time
	busy    bool
}

// CheckoutBroker holds only hashed bearer capabilities, scoped to one live Job
// lease and immutable commit. Restart invalidates capabilities, never broadens them.
type CheckoutBroker struct {
	service *Service
	leases  CheckoutLeaseStore
	mu      sync.Mutex
	entries map[[32]byte]*checkoutEntry
	http    *http.Client
}

func NewCheckoutBroker(s *Service, leases CheckoutLeaseStore) *CheckoutBroker {
	return &CheckoutBroker{service: s, leases: leases, entries: make(map[[32]byte]*checkoutEntry), http: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRemote }}}
}
func (b *CheckoutBroker) IssueAssignmentCredential(ctx context.Context, runner uuid.UUID, a *runmodel.Assignment) (githubapp.CheckoutCredential, error) {
	if a == nil || a.Source == nil || a.Source.Provider != "gitee" || !identity.ValidGitHubSubject(a.Source.RepositoryID) || !shaPattern.MatchString(a.Source.CommitSHA) || b.leases == nil || b.service == nil {
		return githubapp.CheckoutCredential{}, githubapp.ErrCredentialUnavailable
	}
	lease := runmodel.LeaseRequest{RunnerID: runner, JobID: a.JobID, LeaseToken: a.LeaseToken}
	if err := b.leases.CheckGiteeCheckoutLease(ctx, lease, a.Source.RepositoryUUID, a.Source.CommitSHA); err != nil {
		return githubapp.CheckoutCredential{}, githubapp.ErrCredentialUnavailable
	}
	token := []byte(identity.NewToken())
	expires := time.Now().Add(2 * time.Minute)
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, e := range b.entries {
		if time.Now().After(e.expires) {
			delete(b.entries, key)
		}
	}
	if len(b.entries) >= 1000 {
		clear(token)
		return githubapp.CheckoutCredential{}, ErrBusy
	}
	b.entries[sha256.Sum256(token)] = &checkoutEntry{lease: lease, source: *a.Source, expires: expires}
	return githubapp.CheckoutCredential{RepositoryID: a.Source.RepositoryID, Token: token, ExpiresAt: expires, CloneURL: b.service.Origin + "/api/v1/checkout/gitee/" + a.Source.RepositoryID + ".git"}, nil
}

func (b *CheckoutBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	deny := func() { http.Error(w, "checkout unavailable", http.StatusForbidden) }
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || len(auth[0]) != 50 || r.URL.RawPath != "" || r.Header.Get("Git-Protocol") != "" {
		deny()
		return
	}
	key := sha256.Sum256([]byte(strings.TrimPrefix(auth[0], "Bearer ")))
	b.mu.Lock()
	entry := b.entries[key]
	if entry == nil || entry.busy || !entry.expires.After(time.Now()) {
		b.mu.Unlock()
		deny()
		return
	}
	entry.busy = true
	e := *entry
	b.mu.Unlock()
	defer func() { b.mu.Lock(); entry.busy = false; b.mu.Unlock() }()
	base := "/api/v1/checkout/gitee/" + e.source.RepositoryID + ".git"
	suffix := ""
	switch {
	case r.Method == "GET" && r.URL.Path == base+"/info/refs" && r.URL.RawQuery == "service=git-upload-pack":
		suffix = "/info/refs?service=git-upload-pack"
	case r.Method == "POST" && r.URL.Path == base+"/git-upload-pack" && r.URL.RawQuery == "" && r.Header.Get("Content-Type") == "application/x-git-upload-pack-request":
		suffix = "/git-upload-pack"
	default:
		deny()
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), minTime(e.expires, time.Now().Add(45*time.Second)))
	defer cancel()
	check := func() error {
		return b.leases.CheckGiteeCheckoutLease(ctx, e.lease, e.source.RepositoryUUID, e.source.CommitSHA)
	}
	if check() != nil {
		deny()
		return
	}
	var body []byte
	if r.Method == "POST" {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
		defer clear(body)
		if err != nil || len(body) > 64<<10 || !validUpload(body, e.source.CommitSHA) {
			deny()
			return
		}
	}
	store, ok := b.service.Store.(AutomationStore)
	if !ok {
		deny()
		return
	}
	binding, err := store.ResolveGiteeRepository(ctx, e.source.RepositoryID)
	if err != nil || binding.ProjectID != e.source.RepositoryUUID || !ValidComponent(binding.Owner) || !ValidComponent(binding.Name) {
		deny()
		return
	}
	token, err := b.service.Access(ctx, binding.GrantID)
	if err != nil {
		deny()
		return
	}
	defer clear(token)
	provider, ok := b.service.Provider.(RepositoryProvider)
	if !ok {
		deny()
		return
	}
	remote, err := provider.Repository(ctx, string(token), binding.Owner, binding.Name)
	if err != nil || remote.ID != binding.ID || remote.AccountID != binding.AccountID {
		deny()
		return
	}
	user, err := b.service.Provider.User(ctx, string(token))
	if err != nil || !ValidComponent(user.Login) {
		deny()
		return
	}
	grant, _, err := b.service.Store.GiteeGrant(ctx, binding.GrantID)
	if err != nil || user.Subject != grant.Subject {
		deny()
		return
	}
	// The upstream hostname and repository path are trusted database/provider data;
	// never forward caller headers, credentials, cookies, redirects or arbitrary URLs.
	upstream, err := http.NewRequestWithContext(ctx, r.Method, identity.GiteeInstance+"/"+binding.Owner+"/"+binding.Name+".git"+suffix, bytes.NewReader(body))
	if err != nil {
		deny()
		return
	}
	upstream.SetBasicAuth(user.Login, string(token))
	upstream.Header.Set("User-Agent", "YuanCI-checkout")
	if r.Method == "POST" {
		upstream.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	}
	if check() != nil {
		deny()
		return
	}
	// A cancelled Job, disabled Runner, revoked grant or expired lease terminates
	// an in-flight transfer too, within the one-second revalidation interval.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if check() != nil {
					cancel()
					return
				}
			}
		}
	}()
	response, err := b.http.Do(upstream)
	if err != nil {
		deny()
		return
	}
	defer response.Body.Close()
	expected := "application/x-git-upload-pack-result"
	if r.Method == "GET" {
		expected = "application/x-git-upload-pack-advertisement"
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != expected {
		deny()
		return
	}
	w.Header().Set("Content-Type", expected)
	// Truncation is intentionally a failed checkout; never an unbounded transfer.
	_, _ = io.Copy(w, io.LimitReader(response.Body, 128<<20))
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Only the initial protocol-v0 depth-one fetch of the assigned SHA is allowed.
// No push, protocol-v2 commands, arbitrary wants, filters or deepen traversal.
func validUpload(body []byte, sha string) bool {
	wants, depth, done := 0, 0, false
	for len(body) > 0 {
		if len(body) < 4 {
			return false
		}
		n, err := strconv.ParseUint(string(body[:4]), 16, 16)
		if err != nil {
			return false
		}
		body = body[4:]
		if n == 0 {
			continue
		}
		if n < 4 || int(n)-4 > len(body) || done {
			return false
		}
		line := strings.TrimSuffix(string(body[:int(n)-4]), "\n")
		body = body[int(n)-4:]
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return false
		}
		switch fields[0] {
		case "want":
			wants++
			if wants != 1 || len(fields) < 2 || fields[1] != sha {
				return false
			}
			for _, capability := range fields[2:] {
				switch capability {
				case "multi_ack", "multi_ack_detailed", "no-done", "side-band", "side-band-64k", "thin-pack", "no-progress", "ofs-delta", "deepen-since", "deepen-not":
				default:
					if !strings.HasPrefix(capability, "agent=git/") {
						return false
					}
				}
			}
		case "deepen":
			depth++
			if wants != 1 || depth != 1 || line != "deepen 1" {
				return false
			}
		case "done":
			if line != "done" {
				return false
			}
			done = true
		default:
			return false
		}
	}
	return wants == 1 && depth == 1
}
