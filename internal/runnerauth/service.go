package runnerauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultRegistrationTTL = 10 * time.Minute
	MaximumRegistrationTTL = 24 * time.Hour
	RotationGracePeriod    = 15 * time.Minute
)

var (
	ErrDenied        = errors.New("Runner identity denied")
	ErrInvalidInput  = errors.New("invalid Runner identity input")
	runnerNameRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	labelRegexp      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

type Capabilities struct {
	OS                 string
	Architecture       string
	Executor           string
	IsolationLevel     string
	Labels             map[string]string
	Capacity           int
	AvailableDiskBytes int64
	ProtocolVersion    int
	RunnerVersion      string
}

type RegistrationToken struct {
	ID        uuid.UUID
	PoolName  string
	Digest    [32]byte
	ExpiresAt time.Time
	MaxUses   int
	CreatedBy *uuid.UUID
}

type Enrollment struct {
	TokenDigest  [32]byte
	RunnerID     uuid.UUID
	Name         string
	Capabilities Capabilities
	Certificate  CertificateRecord
}

type CertificateRecord struct {
	Serial               string
	CSRFingerprint       [32]byte
	PublicKeyFingerprint [32]byte
	ChainPEM             []byte
	NotBefore            time.Time
	NotAfter             time.Time
}

type Identity struct {
	RunnerID      uuid.UUID
	PoolID        uuid.UUID
	PoolType      string
	Name          string
	CertificateID uuid.UUID
	Serial        string
	Capabilities  Capabilities
}

type Rotation struct {
	RunnerID    uuid.UUID
	OldSerial   string
	Certificate CertificateRecord
	GracePeriod time.Duration
}

type Store interface {
	CreateRegistrationToken(context.Context, RegistrationToken) error
	EnrollRunner(context.Context, Enrollment) (Identity, error)
	AuthenticateRunner(context.Context, uuid.UUID, string) (Identity, error)
	RotateRunnerCertificate(context.Context, Rotation) (CertificateRecord, error)
	DisableRunner(context.Context, uuid.UUID, string, *uuid.UUID) error
	RevokeRunnerCertificate(context.Context, string, string, *uuid.UUID) error
}

type Service struct {
	store     Store
	issuer    *x509.Certificate
	issuerKey crypto.Signer
	now       func() time.Time
	certLife  time.Duration
}

func New(store Store, issuer *x509.Certificate, issuerKey crypto.Signer) (*Service, error) {
	if store == nil || issuer == nil || issuerKey == nil {
		return nil, ErrInvalidInput
	}
	return &Service{store: store, issuer: issuer, issuerKey: issuerKey, now: time.Now,
		certLife: DefaultRunnerLifetime}, nil
}

// NewRegistrationToken creates the only plaintext copy of a registration
// token. Callers must persist only Digest and must durably write Token once.
func NewRegistrationToken() (token string, digest [32]byte, err error) {
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", digest, errors.New("cannot generate Runner registration token")
	}
	token = hex.EncodeToString(raw[:])
	digest = sha256.Sum256([]byte(token))
	return token, digest, nil
}

func TokenDigest(token string) ([32]byte, error) {
	var zero [32]byte
	if len(token) != 64 {
		return zero, ErrDenied
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return zero, ErrDenied
	}
	return sha256.Sum256([]byte(token)), nil
}

func (s *Service) IssueToken(ctx context.Context, pool string, ttl time.Duration, maxUses int, actor *uuid.UUID) (string, RegistrationToken, error) {
	pool = strings.TrimSpace(pool)
	if pool == "" || len(pool) > 128 || ttl < time.Minute || ttl > MaximumRegistrationTTL || maxUses < 1 || maxUses > 256 {
		return "", RegistrationToken{}, ErrInvalidInput
	}
	token, digest, err := NewRegistrationToken()
	if err != nil {
		return "", RegistrationToken{}, err
	}
	record := RegistrationToken{ID: uuid.New(), PoolName: pool, Digest: digest,
		ExpiresAt: s.now().UTC().Add(ttl), MaxUses: maxUses, CreatedBy: actor}
	if err := s.store.CreateRegistrationToken(ctx, record); err != nil {
		return "", RegistrationToken{}, err
	}
	return token, record, nil
}

func (s *Service) Enroll(ctx context.Context, token, name string, capabilities Capabilities, csrPEM []byte) (Identity, CertificateRecord, error) {
	digest, err := TokenDigest(token)
	if err != nil || validateCapabilities(name, capabilities) != nil {
		return Identity{}, CertificateRecord{}, ErrDenied
	}
	runnerID := uuid.New()
	issued, err := SignRunnerCertificate(csrPEM, runnerID, s.issuer, s.issuerKey, s.now(), s.certLife)
	if err != nil {
		return Identity{}, CertificateRecord{}, ErrDenied
	}
	record := certificateRecord(issued)
	identity, err := s.store.EnrollRunner(ctx, Enrollment{TokenDigest: digest, RunnerID: runnerID,
		Name: name, Capabilities: capabilities, Certificate: record})
	if err != nil {
		return Identity{}, CertificateRecord{}, ErrDenied
	}
	return identity, record, nil
}

func (s *Service) Authenticate(ctx context.Context, certificate *x509.Certificate) (Identity, error) {
	runnerID, serial, err := CertificateIdentity(certificate)
	if err != nil {
		return Identity{}, ErrDenied
	}
	identity, err := s.store.AuthenticateRunner(ctx, runnerID, serial)
	if err != nil {
		return Identity{}, ErrDenied
	}
	return identity, nil
}

func (s *Service) Rotate(ctx context.Context, current Identity, csrPEM []byte) (CertificateRecord, error) {
	if current.RunnerID == uuid.Nil || current.Serial == "" {
		return CertificateRecord{}, ErrDenied
	}
	issued, err := SignRunnerCertificate(csrPEM, current.RunnerID, s.issuer, s.issuerKey, s.now(), s.certLife)
	if err != nil {
		return CertificateRecord{}, ErrDenied
	}
	record, err := s.store.RotateRunnerCertificate(ctx, Rotation{RunnerID: current.RunnerID,
		OldSerial: current.Serial, Certificate: certificateRecord(issued), GracePeriod: RotationGracePeriod})
	if err != nil {
		return CertificateRecord{}, ErrDenied
	}
	return record, nil
}

func CertificateIdentity(certificate *x509.Certificate) (uuid.UUID, string, error) {
	if certificate == nil || certificate.SerialNumber == nil || certificate.SerialNumber.Sign() <= 0 ||
		len(certificate.URIs) != 1 || len(certificate.DNSNames) != 0 || len(certificate.IPAddresses) != 0 ||
		len(certificate.EmailAddresses) != 0 || certificate.Subject.String() != "" || certificate.IsCA ||
		!certificate.BasicConstraintsValid || certificate.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(certificate.UnknownExtKeyUsage) != 0 || len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		return uuid.Nil, "", ErrDenied
	}
	identity := certificate.URIs[0]
	if identity == nil || identity.Scheme != "yuanci" || identity.Host != "runner" || identity.RawQuery != "" || identity.Fragment != "" || identity.User != nil {
		return uuid.Nil, "", ErrDenied
	}
	if identity.EscapedPath() != identity.Path {
		return uuid.Nil, "", ErrDenied
	}
	runnerID, err := uuid.Parse(strings.TrimPrefix(identity.Path, "/"))
	if err != nil || identity.Path != "/"+runnerID.String() {
		return uuid.Nil, "", ErrDenied
	}
	serial := strings.ToLower(certificate.SerialNumber.Text(16))
	if len(serial) < 16 || len(serial) > 64 {
		return uuid.Nil, "", ErrDenied
	}
	return runnerID, serial, nil
}

func validateCapabilities(name string, capability Capabilities) error {
	if !runnerNameRegexp.MatchString(name) || len(capability.OS) < 1 || len(capability.OS) > 64 ||
		len(capability.Architecture) < 1 || len(capability.Architecture) > 64 ||
		len(capability.Executor) < 1 || len(capability.Executor) > 64 ||
		(capability.IsolationLevel != "standard" && capability.IsolationLevel != "privileged" && capability.IsolationLevel != "deployment") ||
		capability.Capacity < 1 || capability.Capacity > 256 || capability.AvailableDiskBytes < 0 ||
		capability.ProtocolVersion < 1 || capability.ProtocolVersion > 1024 || len(capability.RunnerVersion) < 1 || len(capability.RunnerVersion) > 128 || len(capability.Labels) > 128 {
		return ErrInvalidInput
	}
	for key, value := range capability.Labels {
		if !labelRegexp.MatchString(key) || len(value) > 256 {
			return ErrInvalidInput
		}
	}
	return nil
}

func certificateRecord(issued IssuedCertificate) CertificateRecord {
	return CertificateRecord{Serial: strings.ToLower(issued.Certificate.SerialNumber.Text(16)),
		CSRFingerprint: issued.CSRFingerprint, PublicKeyFingerprint: issued.PublicKeyFingerprint,
		ChainPEM: append([]byte(nil), issued.ChainPEM...), NotBefore: issued.Certificate.NotBefore, NotAfter: issued.Certificate.NotAfter}
}
