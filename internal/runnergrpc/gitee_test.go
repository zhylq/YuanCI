package runnergrpc

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/githubapp"
	runmodel "github.com/yuanci/yuanci/internal/run"
)

type giteeIssuerStub struct {
	credentialIssuerStub
	runner     uuid.UUID
	assignment *runmodel.Assignment
}

func (s *giteeIssuerStub) IssueAssignmentCredential(_ context.Context, runner uuid.UUID, a *runmodel.Assignment) (githubapp.CheckoutCredential, error) {
	s.runner = runner
	s.assignment = a
	return s.credential, nil
}
func TestGiteeAssignmentUsesLeaseAwareBroker(t *testing.T) {
	runner := uuid.New()
	a := sourceAssignment(uuid.New())
	a.Source.Provider = "gitee"
	a.Source.CloneURL = "https://gitee.com/owner/repo.git"
	issuer := &giteeIssuerStub{credentialIssuerStub: credentialIssuerStub{credential: githubapp.CheckoutCredential{RepositoryID: a.Source.RepositoryID, Token: []byte("short-lived-broker-capability"), ExpiresAt: time.Now().Add(time.Minute), CloneURL: "https://ci.test/api/v1/checkout/gitee/70.git"}}}
	server := &Server{credentials: issuer, jobs: &assignmentJobStore{}}
	message := &runnerv1.JobAssignment{}
	if err := server.attachSourceCredential(t.Context(), runner, a, message); err != nil {
		t.Fatal(err)
	}
	if issuer.runner != runner || issuer.assignment != a || issuer.repository != uuid.Nil || message.Source.CloneUrl != issuer.credential.CloneURL || string(message.Credential.Token) != "short-lived-broker-capability" {
		t.Fatal("Gitee did not use scoped broker")
	}
	if a.Source.CloneURL != "https://gitee.com/owner/repo.git" {
		t.Fatal("persistent source mutated")
	}
	clear(message.Credential.Token)
}
