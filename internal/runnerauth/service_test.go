package runnerauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
)

type identityStoreStub struct {
	enrollment Enrollment
	identity   Identity
	rotated    CertificateRecord
	err        error
}

func (*identityStoreStub) CreateRegistrationToken(context.Context, RegistrationToken) error {
	return nil
}
func (store *identityStoreStub) EnrollRunner(_ context.Context, enrollment Enrollment) (Identity, error) {
	store.enrollment = enrollment
	if store.err != nil {
		return Identity{}, store.err
	}
	store.identity.RunnerID = enrollment.RunnerID
	store.identity.Serial = enrollment.Certificate.Serial
	return store.identity, nil
}
func (store *identityStoreStub) AuthenticateRunner(_ context.Context, id uuid.UUID, serial string) (Identity, error) {
	if store.err != nil || id != store.identity.RunnerID || serial != store.identity.Serial {
		return Identity{}, ErrDenied
	}
	return store.identity, nil
}
func (store *identityStoreStub) RotateRunnerCertificate(_ context.Context, rotation Rotation) (CertificateRecord, error) {
	if store.err != nil {
		return CertificateRecord{}, store.err
	}
	store.rotated = rotation.Certificate
	return rotation.Certificate, nil
}
func (*identityStoreStub) DisableRunner(context.Context, uuid.UUID, string, *uuid.UUID) error {
	return nil
}
func (*identityStoreStub) RevokeRunnerCertificate(context.Context, string, string, *uuid.UUID) error {
	return nil
}

func TestServiceEnrollAuthenticateAndRotateUsesCertificateIdentity(t *testing.T) {
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	root, err := newRootCertificate(now, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := newIntermediateCertificate(now, 36*time.Hour, root)
	if err != nil {
		t.Fatal(err)
	}
	store := &identityStoreStub{identity: Identity{Name: "runner-1"}}
	service, err := New(store, issuer.certificate, issuer.signer)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	csr := makeCSR(t, key, &x509.CertificateRequest{})
	token, _, _ := NewRegistrationToken()
	capability := Capabilities{OS: "linux", Architecture: "amd64", Executor: "docker", IsolationLevel: "standard",
		Labels: map[string]string{"region": "test"}, Capacity: 2, AvailableDiskBytes: 1024, ProtocolVersion: 1, RunnerVersion: "test"}
	identity, certificate, err := service.Enroll(t.Context(), token, "runner-1", capability, csr)
	if err != nil {
		t.Fatal(err)
	}
	if identity.RunnerID == uuid.Nil || store.enrollment.TokenDigest == ([32]byte{}) || certificate.Serial == "" {
		t.Fatal("enrollment identity was not bound")
	}
	store.identity = identity
	if authenticated, err := service.Authenticate(t.Context(), certificateFromRecord(t, certificate)); err != nil || authenticated.RunnerID != identity.RunnerID {
		t.Fatal("certificate identity did not authenticate")
	}
	_, replacementKey, _ := ed25519.GenerateKey(rand.Reader)
	replacementCSR := makeCSR(t, replacementKey, &x509.CertificateRequest{})
	rotated, err := service.Rotate(t.Context(), identity, replacementCSR)
	if err != nil || rotated.CSRFingerprint == certificate.CSRFingerprint || store.rotated.Serial != rotated.Serial {
		t.Fatal("certificate rotation failed")
	}
}

func TestServiceReturnsGenericDenialAndRejectsSpoofedCertificates(t *testing.T) {
	if _, err := TokenDigest("not-a-token"); !errors.Is(err, ErrDenied) {
		t.Fatal("malformed token did not use generic denial")
	}
	certificate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               x509.Certificate{}.Subject,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	if _, _, err := CertificateIdentity(certificate); !errors.Is(err, ErrDenied) {
		t.Fatal("certificate without URI identity accepted")
	}

	// A valid certificate cannot use a body-supplied identity: the only identity
	// accepted here is the exact canonical URI SAN signed into the leaf.
	now := time.Now().UTC()
	root, _ := newRootCertificate(now, 48*time.Hour)
	issuer, _ := newIntermediateCertificate(now, 36*time.Hour, root)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	issued, err := SignRunnerCertificate(makeCSR(t, key, &x509.CertificateRequest{}), uuid.New(), issuer.certificate, issuer.signer, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issued.Certificate.DNSNames = []string{"spoof.example"}
	if _, _, err := CertificateIdentity(issued.Certificate); !errors.Is(err, ErrDenied) {
		t.Fatal("certificate with alternate identity accepted")
	}
}

func certificateFromRecord(t *testing.T, record CertificateRecord) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(record.ChainPEM)
	if block == nil {
		t.Fatal("certificate PEM missing")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}
