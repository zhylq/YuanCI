package runnergrpc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/githubapp"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type credentialIssuerStub struct {
	credential githubapp.CheckoutCredential
	err        error
	repository uuid.UUID
	external   string
}

func (issuer *credentialIssuerStub) IssueCheckoutCredential(_ context.Context, repository uuid.UUID, external string) (githubapp.CheckoutCredential, error) {
	issuer.repository = repository
	issuer.external = external
	return issuer.credential, issuer.err
}

type assignmentJobStore struct {
	assignment *runmodel.Assignment
	released   []runmodel.LeaseRequest
	completed  []runmodel.RunnerCompletion
}

func (store *assignmentJobStore) ClaimRunnerJob(context.Context, runmodel.RunnerClaim) (*runmodel.Assignment, error) {
	assignment := store.assignment
	store.assignment = nil
	return assignment, nil
}
func (*assignmentJobStore) AcknowledgeRunnerJob(context.Context, runmodel.LeaseRequest) (runmodel.LeaseState, error) {
	return runmodel.LeaseState{}, nil
}
func (*assignmentJobStore) StartRunnerJob(context.Context, runmodel.LeaseRequest) (runmodel.LeaseState, error) {
	return runmodel.LeaseState{}, nil
}
func (*assignmentJobStore) RenewRunnerLeases(context.Context, runmodel.HeartbeatRequest) (runmodel.HeartbeatResult, error) {
	return runmodel.HeartbeatResult{}, nil
}
func (store *assignmentJobStore) ReleaseRunnerJob(_ context.Context, request runmodel.LeaseRequest) error {
	store.released = append(store.released, request)
	return nil
}
func (store *assignmentJobStore) CompleteRunnerJob(_ context.Context, request runmodel.RunnerCompletion) error {
	store.completed = append(store.completed, request)
	return nil
}

type captureWorkStream struct {
	grpc.ServerStream
	ctx       context.Context
	responses []*runnerv1.WorkResponse
}

func (stream *captureWorkStream) Context() context.Context      { return stream.ctx }
func (*captureWorkStream) Recv() (*runnerv1.WorkRequest, error) { return nil, io.EOF }
func (stream *captureWorkStream) Send(response *runnerv1.WorkResponse) error {
	stream.responses = append(stream.responses, proto.Clone(response).(*runnerv1.WorkResponse))
	return nil
}

type runnerStoreStub struct {
	expectedToken [32]byte
	identity      runnerauth.Identity
	certificate   runnerauth.CertificateRecord
	disabled      bool
}

func (*runnerStoreStub) CreateRegistrationToken(context.Context, runnerauth.RegistrationToken) error {
	return nil
}
func (store *runnerStoreStub) EnrollRunner(_ context.Context, enrollment runnerauth.Enrollment) (runnerauth.Identity, error) {
	if enrollment.TokenDigest != store.expectedToken {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	store.certificate = enrollment.Certificate
	store.identity = runnerauth.Identity{RunnerID: enrollment.RunnerID, PoolID: uuid.New(), PoolType: "standard",
		Name: enrollment.Name, CertificateID: uuid.New(), Serial: enrollment.Certificate.Serial, Capabilities: enrollment.Capabilities}
	return store.identity, nil
}
func (store *runnerStoreStub) AuthenticateRunner(_ context.Context, id uuid.UUID, serial string) (runnerauth.Identity, error) {
	if store.disabled || id != store.identity.RunnerID || serial != store.identity.Serial {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	return store.identity, nil
}
func (store *runnerStoreStub) RotateRunnerCertificate(_ context.Context, rotation runnerauth.Rotation) (runnerauth.CertificateRecord, error) {
	if rotation.RunnerID != store.identity.RunnerID || rotation.OldSerial != store.identity.Serial {
		return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
	}
	rotation.Certificate.PreviousValidUntil = time.Now().Add(rotation.GracePeriod)
	store.certificate = rotation.Certificate
	return rotation.Certificate, nil
}
func (store *runnerStoreStub) DisableRunner(context.Context, uuid.UUID, string, *uuid.UUID) error {
	store.disabled = true
	return nil
}
func (*runnerStoreStub) RevokeRunnerCertificate(context.Context, string, string, *uuid.UUID) error {
	return nil
}

func TestRealTLSRegistrationAuthenticationAndRotation(t *testing.T) {
	fixture := newTLSFixture(t)
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixture.store.expectedToken, _ = runnerauth.TokenDigest(token)

	unauthenticated := dialRunner(t, fixture.address, fixture.rootPool, "server", nil)
	defer unauthenticated.Close()
	client := runnerv1.NewRunnerServiceClient(unauthenticated)
	runnerKey, csr := newCSR(t)
	response, err := client.Register(t.Context(), &runnerv1.RegisterRequest{OneTimeToken: token, Name: "runner-1",
		ProtocolVersion: 1, CsrPem: csr, Capabilities: testCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	if response.RunnerId != fixture.store.identity.RunnerID.String() || len(response.CertificateChainPem) == 0 || len(response.CaCertificatePem) == 0 {
		t.Fatal("registration response is not certificate-bound")
	}
	if _, err := client.RotateCertificate(t.Context(), &runnerv1.RotateCertificateRequest{ProtocolVersion: 1, CsrPem: csr}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("rotation without client certificate returned %v", err)
	}

	runnerCertificate := tlsCertificate(t, response.CertificateChainPem, runnerKey)
	authenticated := dialRunner(t, fixture.address, fixture.rootPool, "server", &runnerCertificate)
	defer authenticated.Close()
	authenticatedClient := runnerv1.NewRunnerServiceClient(authenticated)
	_, replacementCSR := newCSR(t)
	rotation, err := authenticatedClient.RotateCertificate(t.Context(), &runnerv1.RotateCertificateRequest{ProtocolVersion: 1, CsrPem: replacementCSR})
	if err != nil || len(rotation.CertificateChainPem) == 0 || rotation.PreviousCertificateValidUntil == nil {
		t.Fatalf("authenticated rotation failed: %v", err)
	}
	work, err := authenticatedClient.Work(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	streamErr := work.Send(&runnerv1.WorkRequest{})
	if streamErr == nil {
		_, streamErr = work.Recv()
	}
	if status.Code(streamErr) != codes.InvalidArgument {
		t.Fatalf("authenticated Work did not validate message: %v", streamErr)
	}
	fixture.store.disabled = true
	if _, err := authenticatedClient.RotateCertificate(t.Context(), &runnerv1.RotateCertificateRequest{ProtocolVersion: 1, CsrPem: replacementCSR}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("disabled Runner returned %v", err)
	}
}

func TestWorkStreamAssignsRenewsAndCompletesWithCertificateIdentity(t *testing.T) {
	fixture := newTLSFixture(t)
	token := "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixture.store.expectedToken, _ = runnerauth.TokenDigest(token)
	plain := dialRunner(t, fixture.address, fixture.rootPool, "server", nil)
	key, csr := newCSR(t)
	registration, err := runnerv1.NewRunnerServiceClient(plain).Register(t.Context(), &runnerv1.RegisterRequest{
		OneTimeToken: token, Name: "work-runner", ProtocolVersion: 1, CsrPem: csr, Capabilities: testCapabilities()})
	plain.Close()
	if err != nil {
		t.Fatal(err)
	}
	certificate := tlsCertificate(t, registration.CertificateChainPem, key)
	connection := dialRunner(t, fixture.address, fixture.rootPool, "server", &certificate)
	defer connection.Close()
	client := runnerv1.NewRunnerServiceClient(connection)
	if _, err := fixture.jobs.Create(t.Context(), storetest.Record(t, 1, false)); err != nil {
		t.Fatal(err)
	}
	work, err := client.Work(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := work.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_Heartbeat{Heartbeat: &runnerv1.Heartbeat{
		Capabilities: testCapabilities(), ProtocolVersion: 1}}}); err != nil {
		t.Fatal(err)
	}
	response, err := work.Recv()
	if err != nil || response.GetAssignment() == nil {
		t.Fatalf("assignment: %#v %v", response, err)
	}
	assignment := response.GetAssignment()
	if assignment.LeaseToken == "" || assignment.JobId == "" || len(assignment.ExecutionPlanJson) == 0 {
		t.Fatal("assignment omitted lease-bound execution plan")
	}
	if err := work.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobAccepted{JobAccepted: &runnerv1.JobAccepted{
		JobId: assignment.JobId, LeaseToken: assignment.LeaseToken}}}); err != nil {
		t.Fatal(err)
	}
	if response, err = work.Recv(); err != nil || response.GetLeaseRenewed() == nil {
		t.Fatalf("receipt response: %#v %v", response, err)
	}
	if err := work.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobStarted{JobStarted: &runnerv1.JobStarted{
		JobId: assignment.JobId, LeaseToken: assignment.LeaseToken}}}); err != nil {
		t.Fatal(err)
	}
	if response, err = work.Recv(); err != nil || response.GetLeaseRenewed() == nil {
		t.Fatalf("start response: %#v %v", response, err)
	}
	if err := work.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_Heartbeat{Heartbeat: &runnerv1.Heartbeat{
		Capabilities: testCapabilities(), ProtocolVersion: 1, ActiveLeases: []*runnerv1.ActiveLease{{
			JobId: assignment.JobId, LeaseToken: assignment.LeaseToken, State: runnerv1.LocalJobState_LOCAL_JOB_STATE_RUNNING}}}}}); err != nil {
		t.Fatal(err)
	}
	if response, err = work.Recv(); err != nil || response.GetLeaseRenewed() == nil {
		t.Fatalf("heartbeat response: %#v %v", response, err)
	}
	if err := work.Send(&runnerv1.WorkRequest{Body: &runnerv1.WorkRequest_JobCompleted{JobCompleted: &runnerv1.JobCompleted{
		JobId: assignment.JobId, LeaseToken: assignment.LeaseToken, Conclusion: runnerv1.JobConclusion_JOB_CONCLUSION_SUCCEEDED}}}); err != nil {
		t.Fatal(err)
	}
	if err := work.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := work.Recv(); err != io.EOF {
		t.Fatalf("close stream: %v", err)
	}
	runs, err := fixture.jobs.List(t.Context(), 1)
	if err != nil || len(runs) != 1 || runs[0].Status != runmodel.StatusSucceeded {
		t.Fatalf("completion not persisted: %#v %v", runs, err)
	}
}

func TestSourceAssignmentIssuesCredentialOnlyForDeliveryAndClearsBuffer(t *testing.T) {
	repository := uuid.New()
	token := []byte("ephemeral-checkout-token")
	expires := time.Now().Add(30 * time.Minute).UTC()
	issuer := &credentialIssuerStub{credential: githubapp.CheckoutCredential{RepositoryID: "70", Token: token, ExpiresAt: expires}}
	jobs := &assignmentJobStore{assignment: sourceAssignment(repository)}
	server := &Server{jobs: jobs, credentials: issuer}
	stream := &captureWorkStream{ctx: t.Context()}
	identity := runnerauth.Identity{RunnerID: uuid.New(), PoolType: "standard", Capabilities: runnerauth.Capabilities{ProtocolVersion: 2}}

	if err := server.handleHeartbeat(stream, identity, sourceHeartbeat()); err != nil {
		t.Fatal(err)
	}
	if issuer.repository != repository || issuer.external != "70" {
		t.Fatalf("credential escaped repository binding: %s %q", issuer.repository, issuer.external)
	}
	if len(stream.responses) != 1 || stream.responses[0].GetAssignment() == nil {
		t.Fatalf("missing assignment response: %#v", stream.responses)
	}
	assignment := stream.responses[0].GetAssignment()
	if assignment.Source == nil || assignment.Source.RepositoryId != "70" || assignment.Credential == nil ||
		!bytes.Equal(assignment.Credential.Token, []byte("ephemeral-checkout-token")) ||
		!assignment.Credential.ExpiresAt.AsTime().Equal(expires) {
		t.Fatalf("invalid source credential delivery: %#v", assignment)
	}
	if !bytes.Equal(token, make([]byte, len(token))) {
		t.Fatal("issued credential buffer was retained after delivery")
	}
}

func TestSourceAssignmentCredentialFailuresReleaseOrFailLeaseWithoutLeak(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		released  int
		completed int
	}{
		{name: "transient provider failure releases", err: errors.New("provider secret-token failed"), released: 1},
		{name: "repository mismatch fails", err: githubapp.ErrRepositoryUnavailable, completed: 1},
		{name: "credential unavailable fails", err: githubapp.ErrCredentialUnavailable, completed: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			partial := []byte("partial-secret-token")
			issuer := &credentialIssuerStub{credential: githubapp.CheckoutCredential{Token: partial}, err: test.err}
			jobs := &assignmentJobStore{assignment: sourceAssignment(uuid.New())}
			server := &Server{jobs: jobs, credentials: issuer}
			err := server.handleHeartbeat(&captureWorkStream{ctx: t.Context()}, runnerauth.Identity{RunnerID: uuid.New(), PoolType: "standard",
				Capabilities: runnerauth.Capabilities{ProtocolVersion: 2}}, sourceHeartbeat())
			if status.Code(err) != codes.Internal || bytes.Contains([]byte(err.Error()), []byte("secret-token")) {
				t.Fatalf("unsafe credential error: %v", err)
			}
			if len(jobs.released) != test.released || len(jobs.completed) != test.completed {
				t.Fatalf("unsafe lease disposition: released=%d completed=%d", len(jobs.released), len(jobs.completed))
			}
			if test.completed == 1 && jobs.completed[0].Status != runmodel.JobFailed {
				t.Fatalf("permanent issuance failure status=%s", jobs.completed[0].Status)
			}
			if !bytes.Equal(partial, make([]byte, len(partial))) {
				t.Fatal("partial credential buffer was retained")
			}
		})
	}
}

func sourceAssignment(repository uuid.UUID) *runmodel.Assignment {
	return &runmodel.Assignment{JobID: uuid.New(), RunID: uuid.New(), LeaseToken: "lease-token",
		LeaseExpires: time.Now().Add(time.Minute), Source: &runmodel.SourceCheckout{RepositoryUUID: repository,
			Provider: "github", RepositoryID: "70", CloneURL: "https://github.com/acme/repo.git", CommitSHA: "0123456789012345678901234567890123456789"}}
}

func sourceHeartbeat() *runnerv1.Heartbeat {
	return &runnerv1.Heartbeat{ProtocolVersion: 2, Capabilities: testCapabilities()}
}

func TestTLSRejectsUnknownIdentityWrongTrustAndOversizedMessages(t *testing.T) {
	fixture := newTLSFixture(t)
	unknownID := uuid.New()
	unknownKey, unknownCSR := newCSR(t)
	issued, err := runnerauth.SignRunnerCertificate(unknownCSR, unknownID, fixture.pki.Issuer, fixture.pki.IssuerKey,
		time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	unknownCertificate := tlsCertificate(t, issued.ChainPEM, unknownKey)
	unknown := dialRunner(t, fixture.address, fixture.rootPool, "server", &unknownCertificate)
	defer unknown.Close()
	_, err = runnerv1.NewRunnerServiceClient(unknown).RotateCertificate(t.Context(), &runnerv1.RotateCertificateRequest{ProtocolVersion: 1, CsrPem: unknownCSR})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unknown CA-valid Runner identity returned %v", err)
	}

	wrongName := dialRunner(t, fixture.address, fixture.rootPool, "not-server", nil)
	defer wrongName.Close()
	_, err = runnerv1.NewRunnerServiceClient(wrongName).Register(t.Context(), &runnerv1.RegisterRequest{})
	if err == nil {
		t.Fatal("wrong Server name completed TLS handshake")
	}
	emptyRoots := x509.NewCertPool()
	wrongCA := dialRunner(t, fixture.address, emptyRoots, "server", nil)
	defer wrongCA.Close()
	_, err = runnerv1.NewRunnerServiceClient(wrongCA).Register(t.Context(), &runnerv1.RegisterRequest{})
	if err == nil {
		t.Fatal("untrusted Server certificate completed TLS handshake")
	}

	plain := dialRunner(t, fixture.address, fixture.rootPool, "server", nil)
	defer plain.Close()
	oversized := make([]byte, maxMessageBytes+1)
	_, err = runnerv1.NewRunnerServiceClient(plain).Register(t.Context(), &runnerv1.RegisterRequest{CsrPem: oversized})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized request returned %v", err)
	}
}

func TestTLSRejectsForgedSANWrongEKUAndExpiredCertificate(t *testing.T) {
	t.Run("forged SAN", func(t *testing.T) {
		fixture := newTLSFixture(t)
		runnerID := uuid.New()
		key, csr := newCSR(t)
		identity, _ := url.Parse("yuanci://runner/" + runnerID.String())
		certificate, parsed := customClientCertificate(t, fixture.pki, key, big.NewInt(0x1234567890abcdef),
			time.Now().Add(-time.Minute), time.Now().Add(time.Hour), []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []*url.URL{identity}, []string{"forged.example"})
		fixture.store.identity = runnerauth.Identity{RunnerID: runnerID, Serial: parsed.SerialNumber.Text(16)}
		connection := dialRunner(t, fixture.address, fixture.rootPool, "server", &certificate)
		defer connection.Close()
		_, err := runnerv1.NewRunnerServiceClient(connection).RotateCertificate(t.Context(), &runnerv1.RotateCertificateRequest{ProtocolVersion: 1, CsrPem: csr})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("forged alternate SAN returned %v", err)
		}
	})

	for name, validity := range map[string]struct {
		notBefore time.Time
		notAfter  time.Time
		eku       []x509.ExtKeyUsage
	}{
		"wrong EKU": {time.Now().Add(-time.Minute), time.Now().Add(time.Hour), []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
		"expired":   {time.Now().Add(-2 * time.Hour), time.Now().Add(-time.Hour), []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newTLSFixture(t)
			runnerID := uuid.New()
			key, csr := newCSR(t)
			identity, _ := url.Parse("yuanci://runner/" + runnerID.String())
			certificate, parsed := customClientCertificate(t, fixture.pki, key, big.NewInt(0x2234567890abcdef),
				validity.notBefore, validity.notAfter, validity.eku, []*url.URL{identity}, nil)
			fixture.store.identity = runnerauth.Identity{RunnerID: runnerID, Serial: parsed.SerialNumber.Text(16)}
			connection := dialRunner(t, fixture.address, fixture.rootPool, "server", &certificate)
			defer connection.Close()
			_, err := runnerv1.NewRunnerServiceClient(connection).RotateCertificate(t.Context(), &runnerv1.RotateCertificateRequest{ProtocolVersion: 1, CsrPem: csr})
			if err == nil {
				t.Fatal("invalid client certificate completed RPC")
			}
		})
	}
}

func TestLoadPKIRejectsSymlinksAndBroadPrivatePermissions(t *testing.T) {
	fixture := createPKIFiles(t)
	if _, err := LoadPKI(fixture.files); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(fixture.files.IssuerKey, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPKI(fixture.files); err == nil {
			t.Fatal("world-readable issuer key accepted")
		}
		if err := os.Chmod(fixture.files.IssuerKey, 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "server-cert-link.pem")
	if err := os.Symlink(fixture.files.ServerCertificate, link); err == nil {
		files := fixture.files
		files.ServerCertificate = link
		if _, err := LoadPKI(files); err == nil {
			t.Fatal("certificate symlink accepted")
		}
	}
}

type tlsFixture struct {
	address  string
	rootPool *x509.CertPool
	pki      PKI
	store    *runnerStoreStub
	jobs     *runmodel.MemoryStore
}

type pkiFileFixture struct{ files PKIFiles }

func newTLSFixture(t *testing.T) tlsFixture {
	t.Helper()
	files := createPKIFiles(t)
	pki, err := LoadPKI(files.files)
	if err != nil {
		t.Fatal(err)
	}
	store := &runnerStoreStub{}
	auth, err := runnerauth.New(store, pki.Issuer, pki.IssuerKey)
	if err != nil {
		t.Fatal(err)
	}
	jobs := runmodel.NewMemoryStore()
	server, err := NewServer(auth, jobs, pki.RootPEM, pki.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pki.RootPEM) {
		t.Fatal("root missing")
	}
	return tlsFixture{address: listener.Addr().String(), rootPool: roots, pki: pki, store: store, jobs: jobs}
}

func createPKIFiles(t *testing.T) pkiFileFixture {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "pki")
	if _, err := runnerauth.InitializePKI(runnerauth.PKIOptions{OutputDir: directory, ServerNames: []string{"server", "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	return pkiFileFixture{files: PKIFiles{
		ServerCertificate: filepath.Join(directory, "server", "server-chain.pem"),
		ServerKey:         filepath.Join(directory, "server", "server-key.pem"),
		ClientCA:          filepath.Join(directory, "server", "root-cert.pem"),
		IssuerCertificate: filepath.Join(directory, "server", "intermediate-cert.pem"),
		IssuerKey:         filepath.Join(directory, "server", "intermediate-key.pem"),
	}}
}

func dialRunner(t *testing.T, address string, roots *x509.CertPool, serverName string, certificate *tls.Certificate) *grpc.ClientConn {
	t.Helper()
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: serverName}
	if certificate != nil {
		config.Certificates = []tls.Certificate{*certificate}
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(config)))
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func newCSR(t *testing.T) (crypto.Signer, []byte) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func tlsCertificate(t *testing.T, chain []byte, key crypto.Signer) tls.Certificate {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	certificate, err := tls.X509KeyPair(chain, privatePEM)
	clear(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func customClientCertificate(t *testing.T, pki PKI, key crypto.Signer, serial *big.Int, notBefore, notAfter time.Time,
	usages []x509.ExtKeyUsage, uris []*url.URL, dnsNames []string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	template := &x509.Certificate{SerialNumber: serial, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, BasicConstraintsValid: true,
		URIs: uris, DNSNames: dnsNames}
	der, err := x509.CreateCertificate(rand.Reader, template, pki.Issuer, key.Public(), pki.IssuerKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pki.Issuer.Raw})...)
	return tlsCertificate(t, chain, key), parsed
}

func testCapabilities() *runnerv1.RunnerCapabilities {
	return &runnerv1.RunnerCapabilities{Os: "linux", Architecture: "amd64", Executor: "docker", Capacity: 2,
		AvailableDiskBytes: 1024, IsolationLevel: runnerv1.IsolationLevel_ISOLATION_LEVEL_STANDARD,
		Labels: map[string]string{"region": "test"}}
}

type loggingJobStore struct {
	assignmentJobStore
	chunk runmodel.LogChunk
	err   error
}

func (s *loggingJobStore) AppendLogChunk(_ context.Context, c runmodel.LogChunk) error {
	s.chunk = c
	s.chunk.Data = append([]byte(nil), c.Data...)
	return s.err
}
func TestLogTransportPersistsBeforeAckAndBindsIdentity(t *testing.T) {
	runnerID, jobID := uuid.New(), uuid.New()
	store := &loggingJobStore{}
	server := &Server{jobs: store}
	stream := &captureWorkStream{ctx: t.Context()}
	message := func() *runnerv1.LogChunk {
		return &runnerv1.LogChunk{JobId: jobID.String(), LeaseToken: "lease", Sequence: 1, StepIndex: 2, Data: []byte("hello"), Stderr: true}
	}
	m := message()
	if err := server.handleLog(stream, runnerID, m); err != nil {
		t.Fatal(err)
	}
	if store.chunk.Lease.RunnerID != runnerID || store.chunk.Stream != "stderr" || string(store.chunk.Data) != "hello" || store.chunk.Step != 2 {
		t.Fatal("incorrect persistence")
	}
	if len(stream.responses) != 1 || stream.responses[0].GetLogAcknowledged().Sequence != 1 {
		t.Fatal("missing ack")
	}
	if bytes.Contains(m.Data, []byte("hello")) {
		t.Fatal("transport buffer retained")
	}
	store.err = errors.New("sensitive database detail")
	if err := server.handleLog(stream, runnerID, message()); status.Code(err) != codes.Unavailable || bytes.Contains([]byte(err.Error()), []byte("sensitive")) {
		t.Fatalf("unsafe failure: %v", err)
	}
	if len(stream.responses) != 1 {
		t.Fatal("acknowledged failed persistence")
	}
	m = message()
	m.Sequence = 1 << 63
	if err := server.handleLog(stream, runnerID, m); status.Code(err) != codes.InvalidArgument {
		t.Fatal("overflow accepted")
	}
}
