package runner

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
	runnerv1 "github.com/yuanci/yuanci/gen/runner/v1"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	credentialKeyFile      = "runner-key.pem"
	credentialCertFile     = "runner-chain.pem"
	credentialRootFile     = "root-cert.pem"
	credentialIdentityFile = "identity.json"
	credentialPendingDir   = ".rotation-pending"
	pendingKeyFile         = "runner-key.pem"
	pendingCSRFile         = "runner.csr.pem"
	certificateRotateAhead = 6 * time.Hour
)

type EnrollmentConfig struct {
	Address      string
	ServerName   string
	RootCAFile   string
	StateDir     string
	Token        string
	TokenFile    string
	Name         string
	Capabilities *runnerv1.RunnerCapabilities
}

type Credentials struct {
	RunnerID    uuid.UUID
	Certificate tls.Certificate
	RootPool    *x509.CertPool
	RootPEM     []byte
	NotAfter    time.Time
}

type RotationConfig struct {
	Address    string
	ServerName string
	StateDir   string
	Current    Credentials
}

type credentialMetadata struct {
	RunnerID string    `json:"runner_id"`
	NotAfter time.Time `json:"not_after"`
}

func LoadOrEnroll(ctx context.Context, config EnrollmentConfig) (Credentials, error) {
	if err := validateEnrollmentConfig(config); err != nil {
		return Credentials{}, err
	}
	loaded, err := LoadCredentials(config.StateDir)
	if err == nil {
		return loaded, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Credentials{}, errors.New("Runner credential state is invalid")
	}
	token, err := enrollmentToken(config)
	if err != nil {
		return Credentials{}, err
	}
	rootPEM, err := readBoundedRegular(config.RootCAFile, 64<<10, false)
	if err != nil {
		return Credentials{}, errors.New("cannot read Runner root certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		return Credentials{}, errors.New("invalid Runner root certificate")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Credentials{}, errors.New("cannot generate Runner identity key")
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{PublicKey: public}, private)
	if err != nil {
		return Credentials{}, errors.New("cannot generate Runner certificate request")
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	connection, err := grpc.NewClient(config.Address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: config.ServerName})))
	if err != nil {
		return Credentials{}, errors.New("cannot create Runner enrollment connection")
	}
	defer connection.Close()
	response, err := runnerv1.NewRunnerServiceClient(connection).Register(ctx, &runnerv1.RegisterRequest{
		OneTimeToken: token, Name: config.Name, Capabilities: config.Capabilities, CsrPem: csrPEM, ProtocolVersion: runnerProtocolVersion})
	if err != nil {
		return Credentials{}, errors.New("Runner enrollment failed")
	}
	clear([]byte(token))
	if response == nil || !bytes.Equal(bytes.TrimSpace(response.CaCertificatePem), bytes.TrimSpace(rootPEM)) {
		return Credentials{}, errors.New("Runner enrollment returned an unexpected trust root")
	}
	credential, err := credentialsFromPEM(response.RunnerId, response.CertificateChainPem, rootPEM, private)
	if err != nil {
		return Credentials{}, err
	}
	if err := persistCredentials(config.StateDir, credential, private); err != nil {
		return Credentials{}, err
	}
	if config.TokenFile != "" {
		if err := os.Remove(config.TokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Credentials{}, errors.New("Runner enrolled but registration token file could not be removed")
		}
	}
	return credential, nil
}

func LoadCredentials(directory string) (Credentials, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return Credentials{}, errors.New("Runner state directory must be absolute")
	}
	if err := validateStateDirectory(directory); err != nil {
		return Credentials{}, err
	}
	keyPEM, err := readBoundedRegular(filepath.Join(directory, credentialKeyFile), 16<<10, true)
	if err != nil {
		return Credentials{}, err
	}
	defer clear(keyPEM)
	chainPEM, err := readBoundedRegular(filepath.Join(directory, credentialCertFile), 64<<10, false)
	if err != nil {
		return Credentials{}, err
	}
	rootPEM, err := readBoundedRegular(filepath.Join(directory, credentialRootFile), 64<<10, false)
	if err != nil {
		return Credentials{}, err
	}
	metadataJSON, err := readBoundedRegular(filepath.Join(directory, credentialIdentityFile), 4<<10, false)
	if err != nil {
		return Credentials{}, err
	}
	var metadata credentialMetadata
	decoder := json.NewDecoder(bytes.NewReader(metadataJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Credentials{}, errors.New("invalid Runner credential metadata")
	}
	block, rest := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return Credentials{}, errors.New("invalid Runner private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	clear(block.Bytes)
	private, ok := key.(ed25519.PrivateKey)
	if err != nil || !ok {
		return Credentials{}, errors.New("invalid Runner private key")
	}
	credential, err := credentialsFromPEM(metadata.RunnerID, chainPEM, rootPEM, private)
	if err != nil || !credential.NotAfter.Equal(metadata.NotAfter) {
		return Credentials{}, errors.New("Runner credential metadata does not match certificate")
	}
	return credential, nil
}

// RotateCredentials creates and durably stores a fresh key/CSR before making
// the RPC. A retry reuses that pending CSR, allowing the server's idempotent
// rotation transaction to recover from a lost response.
func RotateCredentials(ctx context.Context, config RotationConfig) (Credentials, error) {
	if config.Address == "" || config.ServerName == "" || !filepath.IsAbs(config.StateDir) {
		return Credentials{}, errors.New("invalid Runner rotation configuration")
	}
	if _, err := config.Current.TLSConfig(config.ServerName); err != nil {
		return Credentials{}, err
	}
	private, csrPEM, err := loadOrCreatePendingRotation(config.StateDir)
	if err != nil {
		return Credentials{}, err
	}
	tlsConfig, _ := config.Current.TLSConfig(config.ServerName)
	connection, err := grpc.NewClient(config.Address, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return Credentials{}, errors.New("cannot create Runner rotation connection")
	}
	defer connection.Close()
	response, err := runnerv1.NewRunnerServiceClient(connection).RotateCertificate(ctx, &runnerv1.RotateCertificateRequest{
		CsrPem: csrPEM, ProtocolVersion: runnerProtocolVersion})
	if err != nil {
		return Credentials{}, errors.New("Runner certificate rotation failed")
	}
	if response == nil || len(response.CertificateChainPem) == 0 {
		return Credentials{}, errors.New("Runner certificate rotation returned an invalid response")
	}
	rotated, err := credentialsFromPEM(config.Current.RunnerID.String(), response.CertificateChainPem,
		config.Current.RootPEM, private)
	if err != nil {
		return Credentials{}, err
	}
	if err := replaceCredentials(config.StateDir, rotated, private); err != nil {
		return Credentials{}, err
	}
	return rotated, nil
}

func loadOrCreatePendingRotation(stateDir string) (ed25519.PrivateKey, []byte, error) {
	if err := validateStateDirectory(stateDir); err != nil {
		return nil, nil, errors.New("Runner credential state is invalid")
	}
	pending := filepath.Join(stateDir, credentialPendingDir)
	if info, err := os.Lstat(pending); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil, nil, errors.New("Runner pending rotation state is insecure")
		}
		keyPEM, keyErr := readBoundedRegular(filepath.Join(pending, pendingKeyFile), 16<<10, true)
		csrPEM, csrErr := readBoundedRegular(filepath.Join(pending, pendingCSRFile), runnerauth.MaxCSRBytes, false)
		if keyErr != nil || csrErr != nil {
			return nil, nil, errors.New("Runner pending rotation state is invalid")
		}
		defer clear(keyPEM)
		private, err := parseEd25519PrivateKey(keyPEM)
		if err != nil {
			return nil, nil, err
		}
		request, err := runnerauth.ValidateRunnerCSR(csrPEM)
		if err != nil || request.Request == nil {
			return nil, nil, errors.New("Runner pending rotation request is invalid")
		}
		requestPublic, ok := request.Request.PublicKey.(ed25519.PublicKey)
		if !ok || !requestPublic.Equal(private.Public()) {
			return nil, nil, errors.New("Runner pending rotation key and request do not match")
		}
		return private, csrPEM, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, errors.New("cannot inspect Runner pending rotation state")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, errors.New("cannot generate Runner rotation key")
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{PublicKey: public}, private)
	if err != nil {
		return nil, nil, errors.New("cannot generate Runner rotation request")
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, errors.New("cannot encode Runner rotation key")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	clear(privateDER)
	temporary, err := os.MkdirTemp(stateDir, ".rotation-pending-*")
	if err != nil {
		return nil, nil, errors.New("cannot create Runner pending rotation state")
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0700); err != nil {
		return nil, nil, errors.New("cannot secure Runner pending rotation state")
	}
	if err := writeExclusiveAtomic(temporary, pendingKeyFile, keyPEM, 0600); err != nil {
		return nil, nil, err
	}
	clear(keyPEM)
	if err := writeExclusiveAtomic(temporary, pendingCSRFile, csrPEM, 0644); err != nil {
		return nil, nil, err
	}
	if err := os.Rename(temporary, pending); err != nil {
		return nil, nil, errors.New("cannot publish Runner pending rotation state")
	}
	return private, csrPEM, nil
}

func parseEd25519PrivateKey(keyPEM []byte) (ed25519.PrivateKey, error) {
	block, rest := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid Runner private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	clear(block.Bytes)
	private, ok := key.(ed25519.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("invalid Runner private key")
	}
	return private, nil
}

func replaceCredentials(directory string, credential Credentials, private crypto.Signer) error {
	parent := filepath.Dir(directory)
	staging := filepath.Join(parent, "."+filepath.Base(directory)+"-next-"+uuid.NewString())
	if err := persistCredentials(staging, credential, private); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	backup := filepath.Join(parent, "."+filepath.Base(directory)+"-previous-"+uuid.NewString())
	if err := os.Rename(directory, backup); err != nil {
		return errors.New("cannot stage existing Runner credentials")
	}
	if err := os.Rename(staging, directory); err != nil {
		_ = os.Rename(backup, directory)
		return errors.New("cannot activate rotated Runner credentials")
	}
	if err := os.RemoveAll(backup); err != nil {
		return errors.New("rotated Runner credentials activated but old state could not be removed")
	}
	return nil
}

func credentialsFromPEM(rawID string, chainPEM, rootPEM []byte, private crypto.Signer) (Credentials, error) {
	runnerID, err := uuid.Parse(rawID)
	if err != nil || runnerID == uuid.Nil {
		return Credentials{}, errors.New("invalid Runner certificate identity")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return Credentials{}, errors.New("cannot encode Runner identity key")
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	clear(privateDER)
	certificate, err := tls.X509KeyPair(chainPEM, privatePEM)
	clear(privatePEM)
	if err != nil || len(certificate.Certificate) < 2 {
		return Credentials{}, errors.New("Runner certificate does not match local key")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return Credentials{}, errors.New("invalid Runner certificate")
	}
	identityID, _, err := runnerauth.CertificateIdentity(leaf)
	if err != nil || identityID != runnerID || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return Credentials{}, errors.New("invalid Runner certificate identity or validity")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		return Credentials{}, errors.New("invalid Runner trust root")
	}
	intermediates := x509.NewCertPool()
	for _, der := range certificate.Certificate[1:] {
		parsed, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			return Credentials{}, errors.New("invalid Runner certificate chain")
		}
		intermediates.AddCert(parsed)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: time.Now()}); err != nil {
		return Credentials{}, errors.New("Runner certificate chain verification failed")
	}
	certificate.Leaf = leaf
	return Credentials{RunnerID: runnerID, Certificate: certificate, RootPool: roots,
		RootPEM: append([]byte(nil), rootPEM...), NotAfter: leaf.NotAfter}, nil
}

func persistCredentials(directory string, credential Credentials, private crypto.Signer) error {
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return errors.New("cannot create Runner state parent directory")
	}
	if _, err := os.Lstat(directory); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("Runner credential state appeared during enrollment")
	}
	temporary, err := os.MkdirTemp(parent, ".yuanci-runner-state-*")
	if err != nil {
		return errors.New("cannot create temporary Runner state directory")
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0700); err != nil {
		return errors.New("cannot secure temporary Runner state directory")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return errors.New("cannot encode Runner identity key")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	clear(privateDER)
	chainPEM := make([]byte, 0)
	for _, der := range credential.Certificate.Certificate {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	metadata, _ := json.Marshal(credentialMetadata{RunnerID: credential.RunnerID.String(), NotAfter: credential.NotAfter})
	files := []struct {
		name string
		body []byte
		mode os.FileMode
	}{{credentialKeyFile, keyPEM, 0600}, {credentialCertFile, chainPEM, 0644},
		{credentialRootFile, credential.RootPEM, 0644}, {credentialIdentityFile, metadata, 0644}}
	defer clear(keyPEM)
	for _, file := range files {
		if err := writeExclusiveAtomic(temporary, file.name, file.body, file.mode); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, directory); err != nil {
		return errors.New("cannot publish Runner credential state")
	}
	return nil
}

func writeExclusiveAtomic(directory, name string, body []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return errors.New("cannot create Runner credential file")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(body)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	target := filepath.Join(directory, name)
	if err == nil {
		if _, statErr := os.Lstat(target); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			err = errors.New("Runner credential target already exists")
		}
	}
	if err == nil {
		err = os.Rename(temporaryName, target)
	}
	if err != nil {
		return errors.New("cannot persist Runner credentials")
	}
	return nil
}

func validateStateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("Runner state directory is insecure")
	}
	return nil
}

func readBoundedRegular(path string, limit int64, private bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > limit ||
		(private && runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("Runner credential file is insecure")
	}
	return os.ReadFile(path)
}

func enrollmentToken(config EnrollmentConfig) (string, error) {
	if (config.Token == "") == (config.TokenFile == "") {
		return "", errors.New("exactly one Runner registration token source is required")
	}
	if config.Token != "" {
		return config.Token, nil
	}
	value, err := readBoundedRegular(config.TokenFile, 4096, true)
	if err != nil {
		return "", errors.New("cannot read Runner registration token")
	}
	defer clear(value)
	return string(bytes.TrimSpace(value)), nil
}

func validateEnrollmentConfig(config EnrollmentConfig) error {
	if config.Address == "" || config.ServerName == "" || config.RootCAFile == "" || config.StateDir == "" ||
		!filepath.IsAbs(config.StateDir) || config.Name == "" || config.Capabilities == nil {
		return errors.New("invalid Runner enrollment configuration")
	}
	return nil
}

func (credential Credentials) TLSConfig(serverName string) (*tls.Config, error) {
	if credential.RunnerID == uuid.Nil || credential.RootPool == nil || len(credential.Certificate.Certificate) == 0 || serverName == "" {
		return nil, fmt.Errorf("invalid Runner credentials")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: credential.RootPool,
		ServerName: serverName, Certificates: []tls.Certificate{credential.Certificate}}, nil
}
