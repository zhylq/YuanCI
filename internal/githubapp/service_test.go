package githubapp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/project"
	"github.com/yuanci/yuanci/internal/scm"
	"github.com/yuanci/yuanci/internal/secrets"
)

type serviceStore struct {
	repository Repository
	err        error
}

func (s serviceStore) ResolveGitHubRepository(context.Context, string) (Repository, error) {
	return s.repository, s.err
}

type serviceProvider struct {
	token        []byte
	expiry       time.Time
	content      []byte
	calls        int
	keyObserved  string
	installID    string
	repositoryID string
	owner        string
	name         string
	path         string
	sha          string
}

func (p *serviceProvider) InstallationToken(_ context.Context, _ string, key []byte, installID, repositoryID string) ([]byte, time.Time, error) {
	p.calls++
	p.keyObserved = string(key)
	p.installID, p.repositoryID = installID, repositoryID
	return p.token, p.expiry, nil
}

func (p *serviceProvider) RepositoryFile(_ context.Context, _ []byte, owner, name, path, sha string) ([]byte, error) {
	p.owner, p.name, p.path, p.sha = owner, name, path, sha
	return p.content, nil
}

func TestFetchPipelineUsesTrustedRepositoryAndImmutableSHA(t *testing.T) {
	master := []byte(strings.Repeat("m", 32))
	cipher, err := secrets.NewCipher(master)
	if err != nil {
		t.Fatal(err)
	}
	appID := uuid.New()
	envelope, err := cipher.Seal([]byte("private-app-key"), KeyAAD(appID))
	if err != nil {
		t.Fatal(err)
	}
	repository := Repository{ID: uuid.New(), ExternalID: "70", Owner: "trusted", Name: "repository",
		InstallationID: "34", AppID: appID, AppClientID: "Iv1.test", EncryptedKey: envelope}
	token := []byte("temporary-installation-token")
	provider := &serviceProvider{token: token, expiry: time.Now().Add(50 * time.Minute), content: []byte("version: v1")}
	service, err := New(serviceStore{repository: repository}, cipher, provider)
	if err != nil {
		t.Fatal(err)
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	event := scm.Event{Provider: scm.GitHub, Type: scm.EventPush, AfterSHA: sha,
		Repository: scm.Repository{ExternalID: "70", Owner: "webhook-attacker", Name: "wrong"}}
	resolved, content, err := service.FetchPipeline(t.Context(), event, "ci/pipeline.yml")
	if err != nil || resolved.ID != repository.ID || string(content) != "version: v1" {
		t.Fatalf("fetch result: %#v %q %v", resolved, content, err)
	}
	if provider.calls != 1 || provider.keyObserved != "private-app-key" || provider.installID != "34" ||
		provider.repositoryID != "70" || provider.owner != "trusted" || provider.name != "repository" ||
		provider.path != "ci/pipeline.yml" || provider.sha != sha {
		t.Fatalf("untrusted or mutable fetch: %#v", provider)
	}
	if !bytes.Equal(token, make([]byte, len(token))) {
		t.Fatal("installation token buffer was not cleared")
	}
}

func TestFetchPipelineRejectsForkAndInvalidInputsBeforeCredentials(t *testing.T) {
	cipher, _ := secrets.NewCipher([]byte(strings.Repeat("m", 32)))
	provider := &serviceProvider{}
	service, _ := New(serviceStore{err: errors.New("must not resolve")}, cipher, provider)
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, event := range []scm.Event{
		{Provider: scm.GitHub, Type: scm.EventPullRequest, AfterSHA: sha, Repository: scm.Repository{ExternalID: "70"}, Metadata: map[string]string{"fork": "true"}},
		{Provider: scm.GitLab, Type: scm.EventPush, AfterSHA: sha, Repository: scm.Repository{ExternalID: "70"}},
		{Provider: scm.GitHub, Type: scm.EventPush, AfterSHA: "main", Repository: scm.Repository{ExternalID: "70"}},
		{Provider: scm.GitHub, Type: scm.EventPush, AfterSHA: sha, Repository: scm.Repository{ExternalID: "../70"}},
	} {
		if _, _, err := service.FetchPipeline(t.Context(), event, project.DefaultPipelinePath); err == nil {
			t.Fatalf("unsafe event accepted: %#v", event)
		}
	}
	if provider.calls != 0 {
		t.Fatal("credential requested for rejected event")
	}
}

func TestFetchPipelineRejectsUnsafeExpiryAndClearsToken(t *testing.T) {
	cipher, _ := secrets.NewCipher([]byte(strings.Repeat("m", 32)))
	appID := uuid.New()
	envelope, _ := cipher.Seal([]byte("private-app-key"), KeyAAD(appID))
	repository := Repository{ExternalID: "70", Owner: "trusted", Name: "repo", InstallationID: "34",
		AppID: appID, AppClientID: "Iv1.test", EncryptedKey: envelope}
	token := []byte("short-lived-token")
	provider := &serviceProvider{token: token, expiry: time.Now().Add(10 * time.Second)}
	service, _ := New(serviceStore{repository: repository}, cipher, provider)
	event := scm.Event{Provider: scm.GitHub, Type: scm.EventTag,
		AfterSHA: "0123456789abcdef0123456789abcdef01234567", Repository: scm.Repository{ExternalID: "70"}}
	if _, _, err := service.FetchPipeline(t.Context(), event, project.DefaultPipelinePath); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("unsafe expiry accepted: %v", err)
	}
	if !bytes.Equal(token, make([]byte, len(token))) || provider.path != "" {
		t.Fatal("unsafe token retained or used")
	}
}
