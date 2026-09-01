package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"github.com/yuanci/yuanci/internal/runnergrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestLoadOrEnrollPersistsAndReusesLocalIdentity(t *testing.T) {
	fixture := newEnrollmentFixture(t)
	stateDir := filepath.Join(t.TempDir(), "state", "runner")
	tokenFile := filepath.Join(t.TempDir(), "registration-token")
	if err := os.WriteFile(tokenFile, []byte(fixture.token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := EnrollmentConfig{Address: fixture.address, ServerName: "server", RootCAFile: fixture.rootFile,
		StateDir: stateDir, TokenFile: tokenFile, Name: "credential-runner", Capabilities: credentialCapabilities()}
	first, err := LoadOrEnroll(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunnerID == uuid.Nil || first.NotAfter.IsZero() {
		t.Fatal("enrollment did not return a usable identity")
	}
	if _, err := os.Stat(tokenFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registration token file survived enrollment: %v", err)
	}
	second, err := LoadOrEnroll(t.Context(), config)
	if err != nil || second.RunnerID != first.RunnerID {
		t.Fatalf("restart did not reuse identity: %s %v", second.RunnerID, err)
	}
	if fixture.store.enrollments != 1 {
		t.Fatalf("restart enrolled %d times", fixture.store.enrollments)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(stateDir, credentialKeyFile))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private key mode=%v err=%v", info.Mode().Perm(), err)
		}
	}
}

func TestLoadCredentialsRejectsCorruptionSymlinkAndBroadPrivateMode(t *testing.T) {
	fixture := newEnrollmentFixture(t)
	stateDir := filepath.Join(t.TempDir(), "runner")
	config := EnrollmentConfig{Address: fixture.address, ServerName: "server", RootCAFile: fixture.rootFile,
		StateDir: stateDir, Token: fixture.token, Name: "secure-runner", Capabilities: credentialCapabilities()}
	if _, err := LoadOrEnroll(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(stateDir, credentialKeyFile)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(keyFile, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCredentials(stateDir); err == nil {
			t.Fatal("broad private key permissions accepted")
		}
		if err := os.Chmod(keyFile, 0600); err != nil {
			t.Fatal(err)
		}
	}
	metadata := filepath.Join(stateDir, credentialIdentityFile)
	if err := os.WriteFile(metadata, []byte(`{"runner_id":"wrong"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(stateDir); err == nil {
		t.Fatal("corrupt metadata accepted")
	}
	if err := os.Remove(metadata); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.rootFile, metadata); err == nil {
		if _, err := LoadCredentials(stateDir); err == nil {
			t.Fatal("credential symlink accepted")
		}
	}
}

func TestRotateCredentialsReusesPendingCSRWhenResponseWasLost(t *testing.T) {
	fixture := newEnrollmentFixture(t)
	stateDir := filepath.Join(t.TempDir(), "runner")
	current, err := LoadOrEnroll(t.Context(), EnrollmentConfig{Address: fixture.address, ServerName: "server",
		RootCAFile: fixture.rootFile, StateDir: stateDir, Token: fixture.token, Name: "rotation-runner",
		Capabilities: credentialCapabilities()})
	if err != nil {
		t.Fatal(err)
	}
	_, csrPEM, err := loadOrCreatePendingRotation(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := current.TLSConfig("server")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := grpc.NewClient(fixture.address, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a committed server rotation whose response never reached the
	// credential publisher. The pending key/CSR remains on disk.
	if _, err := runnerv1.NewRunnerServiceClient(connection).RotateCertificate(t.Context(),
		&runnerv1.RotateCertificateRequest{CsrPem: csrPEM, ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()

	rotated, err := RotateCredentials(t.Context(), RotationConfig{Address: fixture.address, ServerName: "server",
		StateDir: stateDir, Current: current})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.store.rotations != 1 {
		t.Fatalf("same pending CSR produced %d rotation transactions", fixture.store.rotations)
	}
	if rotated.RunnerID != current.RunnerID || bytes.Equal(rotated.Certificate.Certificate[0], current.Certificate.Certificate[0]) {
		t.Fatal("rotated credentials did not preserve identity with a new certificate")
	}
	loaded, err := LoadCredentials(stateDir)
	if err != nil || !bytes.Equal(loaded.Certificate.Certificate[0], rotated.Certificate.Certificate[0]) {
		t.Fatalf("rotated credentials were not activated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, credentialPendingDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending rotation state survived activation: %v", err)
	}
}

type enrollmentStore struct {
	digest      [32]byte
	identity    runnerauth.Identity
	enrollments int
	previous    string
	rotated     runnerauth.CertificateRecord
	rotations   int
}

func (*enrollmentStore) CreateRegistrationToken(context.Context, runnerauth.RegistrationToken) error {
	return nil
}
func (store *enrollmentStore) EnrollRunner(_ context.Context, enrollment runnerauth.Enrollment) (runnerauth.Identity, error) {
	if enrollment.TokenDigest != store.digest || store.enrollments != 0 {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	store.enrollments++
	store.identity = runnerauth.Identity{RunnerID: enrollment.RunnerID, PoolID: uuid.New(), PoolType: "standard",
		Name: enrollment.Name, CertificateID: uuid.New(), Serial: enrollment.Certificate.Serial, Capabilities: enrollment.Capabilities}
	return store.identity, nil
}
func (store *enrollmentStore) AuthenticateRunner(_ context.Context, id uuid.UUID, serial string) (runnerauth.Identity, error) {
	if id != store.identity.RunnerID || (serial != store.identity.Serial && serial != store.previous) {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	identity := store.identity
	identity.Serial = serial
	return identity, nil
}
func (store *enrollmentStore) RotateRunnerCertificate(_ context.Context, rotation runnerauth.Rotation) (runnerauth.CertificateRecord, error) {
	if rotation.RunnerID != store.identity.RunnerID {
		return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
	}
	if store.rotated.CSRFingerprint == rotation.Certificate.CSRFingerprint && store.rotations > 0 {
		return store.rotated, nil
	}
	if rotation.OldSerial != store.identity.Serial {
		return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
	}
	store.previous = store.identity.Serial
	store.identity.Serial = rotation.Certificate.Serial
	store.rotated = rotation.Certificate
	store.rotated.PreviousValidUntil = time.Now().Add(rotation.GracePeriod)
	store.rotations++
	return store.rotated, nil
}
func (*enrollmentStore) DisableRunner(context.Context, uuid.UUID, string, *uuid.UUID) error {
	return nil
}
func (*enrollmentStore) RevokeRunnerCertificate(context.Context, string, string, *uuid.UUID) error {
	return nil
}

type enrollmentFixture struct {
	address  string
	rootFile string
	token    string
	store    *enrollmentStore
	jobs     *runmodel.MemoryStore
}

func newEnrollmentFixture(t *testing.T) enrollmentFixture {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "pki")
	if _, err := runnerauth.InitializePKI(runnerauth.PKIOptions{OutputDir: directory, ServerNames: []string{"server", "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	pki, err := runnergrpc.LoadPKI(runnergrpc.PKIFiles{ServerCertificate: filepath.Join(directory, "server", "server-chain.pem"),
		ServerKey: filepath.Join(directory, "server", "server-key.pem"), ClientCA: filepath.Join(directory, "server", "root-cert.pem"),
		IssuerCertificate: filepath.Join(directory, "server", "intermediate-cert.pem"), IssuerKey: filepath.Join(directory, "server", "intermediate-key.pem")})
	if err != nil {
		t.Fatal(err)
	}
	token := "2123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	store := &enrollmentStore{digest: sha256.Sum256([]byte(token))}
	auth, err := runnerauth.New(store, pki.Issuer, pki.IssuerKey)
	if err != nil {
		t.Fatal(err)
	}
	jobs := runmodel.NewMemoryStore()
	server, err := runnergrpc.NewServer(auth, jobs, pki.RootPEM, pki.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); listener.Close() })
	return enrollmentFixture{address: listener.Addr().String(), rootFile: filepath.Join(directory, "server", "root-cert.pem"),
		token: token, store: store, jobs: jobs}
}

func credentialCapabilities() *runnerv1.RunnerCapabilities {
	return &runnerv1.RunnerCapabilities{Os: "linux", Architecture: "amd64", Executor: "docker", Capacity: 2,
		AvailableDiskBytes: 1 << 30, IsolationLevel: runnerv1.IsolationLevel_ISOLATION_LEVEL_STANDARD,
		Labels: map[string]string{"region": "test"}}
}

var _ runnerauth.Store = (*enrollmentStore)(nil)
