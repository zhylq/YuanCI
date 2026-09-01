package runnerauth

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	MaxCSRBytes               = 16 << 10
	DefaultRunnerLifetime     = 24 * time.Hour
	defaultRootLifetime       = 10 * 365 * 24 * time.Hour
	defaultIntermediateLife   = 5 * 365 * 24 * time.Hour
	defaultServerLifetime     = 365 * 24 * time.Hour
	certificateClockBackdate  = 5 * time.Minute
	runnerCertificateBackdate = time.Minute
)

var ErrInvalidCSR = errors.New("invalid Runner certificate request")

type PKIOptions struct {
	OutputDir            string
	ServerNames          []string
	Now                  time.Time
	RootLifetime         time.Duration
	IntermediateLifetime time.Duration
	ServerLifetime       time.Duration
}

type CertificateSummary struct {
	SHA256Fingerprint string    `json:"sha256_fingerprint"`
	NotAfter          time.Time `json:"not_after"`
}

type Manifest struct {
	Version      int                `json:"version"`
	CreatedAt    time.Time          `json:"created_at"`
	ServerNames  []string           `json:"server_names"`
	Root         CertificateSummary `json:"root"`
	Intermediate CertificateSummary `json:"intermediate"`
	Server       CertificateSummary `json:"server"`
}

type CSRInfo struct {
	Request              *x509.CertificateRequest
	CSRFingerprint       [32]byte
	PublicKeyFingerprint [32]byte
}

type IssuedCertificate struct {
	Certificate          *x509.Certificate
	ChainPEM             []byte
	CSRFingerprint       [32]byte
	PublicKeyFingerprint [32]byte
}

type generatedCertificate struct {
	certificate    *x509.Certificate
	certificatePEM []byte
	privateKeyPEM  []byte
	signer         crypto.Signer
}

func InitializePKI(options PKIOptions) (manifest Manifest, err error) {
	options, dnsNames, ipAddresses, names, err := normalizePKIOptions(options)
	if err != nil {
		return Manifest{}, err
	}
	outputDir, err := safeNewOutputPath(options.OutputDir)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Mkdir(outputDir, 0700); err != nil {
		return Manifest{}, errors.New("PKI output must have an existing parent and a new target directory")
	}
	created := true
	defer func() {
		if err != nil && created {
			_ = os.RemoveAll(outputDir)
		}
	}()

	rootFS, err := os.OpenRoot(outputDir)
	if err != nil {
		return Manifest{}, errors.New("cannot secure PKI output directory")
	}
	defer rootFS.Close()
	if err := rootFS.Mkdir("offline-root", 0700); err != nil {
		return Manifest{}, errors.New("cannot create offline root directory")
	}
	if err := rootFS.Mkdir("server", 0700); err != nil {
		return Manifest{}, errors.New("cannot create Server PKI directory")
	}

	now := options.Now.UTC().Truncate(time.Second)
	root, err := newRootCertificate(now, options.RootLifetime)
	if err != nil {
		return Manifest{}, errors.New("cannot generate root certificate")
	}
	intermediate, err := newIntermediateCertificate(now, options.IntermediateLifetime, root)
	if err != nil {
		return Manifest{}, errors.New("cannot generate intermediate certificate")
	}
	server, err := newServerCertificate(now, options.ServerLifetime, dnsNames, ipAddresses, intermediate)
	if err != nil {
		return Manifest{}, errors.New("cannot generate Server certificate")
	}

	manifest = Manifest{
		Version:      1,
		CreatedAt:    now,
		ServerNames:  names,
		Root:         summarize(root.certificate),
		Intermediate: summarize(intermediate.certificate),
		Server:       summarize(server.certificate),
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, errors.New("cannot encode PKI manifest")
	}
	manifestJSON = append(manifestJSON, '\n')
	serverChain := append(append([]byte(nil), server.certificatePEM...), intermediate.certificatePEM...)

	files := []struct {
		name string
		mode os.FileMode
		data []byte
	}{
		{"offline-root/root-key.pem", 0600, root.privateKeyPEM},
		{"offline-root/root-cert.pem", 0644, root.certificatePEM},
		{"server/root-cert.pem", 0644, root.certificatePEM},
		{"server/intermediate-key.pem", 0600, intermediate.privateKeyPEM},
		{"server/intermediate-cert.pem", 0644, intermediate.certificatePEM},
		{"server/server-key.pem", 0600, server.privateKeyPEM},
		{"server/server-chain.pem", 0644, serverChain},
		{"server/manifest.json", 0644, manifestJSON},
	}
	for _, file := range files {
		if err := writeAtomic(rootFS, file.name, file.data, file.mode); err != nil {
			return Manifest{}, errors.New("cannot persist PKI; partial output was removed")
		}
	}
	for _, directory := range []string{"offline-root", "server", "."} {
		if err := syncDirectory(rootFS, directory); err != nil {
			return Manifest{}, errors.New("cannot sync PKI; partial output was removed")
		}
	}
	if err := syncHostDirectory(filepath.Dir(outputDir)); err != nil {
		return Manifest{}, errors.New("cannot sync PKI parent; partial output was removed")
	}
	created = false
	return manifest, nil
}

func ValidateRunnerCSR(csrPEM []byte) (CSRInfo, error) {
	if len(csrPEM) == 0 || len(csrPEM) > MaxCSRBytes {
		return CSRInfo{}, ErrInvalidCSR
	}
	block, rest := pem.Decode(csrPEM)
	if block == nil || (block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST") || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return CSRInfo{}, ErrInvalidCSR
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || request.CheckSignature() != nil {
		return CSRInfo{}, ErrInvalidCSR
	}
	if request.Subject.String() != "" || len(request.DNSNames) != 0 || len(request.IPAddresses) != 0 || len(request.EmailAddresses) != 0 || len(request.URIs) != 0 || len(request.Extensions) != 0 || len(request.ExtraExtensions) != 0 || len(request.Attributes) != 0 {
		return CSRInfo{}, ErrInvalidCSR
	}
	if !supportedPublicKey(request.PublicKey) {
		return CSRInfo{}, ErrInvalidCSR
	}
	publicDER, err := x509.MarshalPKIXPublicKey(request.PublicKey)
	if err != nil {
		return CSRInfo{}, ErrInvalidCSR
	}
	return CSRInfo{
		Request:              request,
		CSRFingerprint:       sha256.Sum256(block.Bytes),
		PublicKeyFingerprint: sha256.Sum256(publicDER),
	}, nil
}

func SignRunnerCertificate(csrPEM []byte, runnerID uuid.UUID, issuer *x509.Certificate, issuerKey crypto.Signer, now time.Time, lifetime time.Duration) (IssuedCertificate, error) {
	info, err := ValidateRunnerCSR(csrPEM)
	if err != nil || runnerID == uuid.Nil || issuer == nil || issuerKey == nil || !issuer.IsCA || issuer.KeyUsage&x509.KeyUsageCertSign == 0 {
		return IssuedCertificate{}, errors.New("cannot sign Runner certificate")
	}
	if !publicKeysEqual(issuer.PublicKey, issuerKey.Public()) {
		return IssuedCertificate{}, errors.New("cannot sign Runner certificate")
	}
	if lifetime <= 0 || lifetime > DefaultRunnerLifetime {
		return IssuedCertificate{}, errors.New("invalid Runner certificate lifetime")
	}
	now = now.UTC().Truncate(time.Second)
	if now.Before(issuer.NotBefore) || !now.Before(issuer.NotAfter) {
		return IssuedCertificate{}, errors.New("Runner certificate issuer is not currently valid")
	}
	notBefore := now.Add(-runnerCertificateBackdate)
	if notBefore.Before(issuer.NotBefore) {
		notBefore = issuer.NotBefore
	}
	notAfter := now.Add(lifetime)
	if notAfter.After(issuer.NotAfter) || !notBefore.Before(issuer.NotAfter) {
		return IssuedCertificate{}, errors.New("Runner certificate exceeds issuer lifetime")
	}
	identity, _ := url.Parse("yuanci://runner/" + runnerID.String())
	serial, err := randomSerial()
	if err != nil {
		return IssuedCertificate{}, errors.New("cannot sign Runner certificate")
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  []*url.URL{identity},
		AuthorityKeyId:        issuer.SubjectKeyId,
		SubjectKeyId:          info.PublicKeyFingerprint[:],
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer, info.Request.PublicKey, issuerKey)
	if err != nil {
		return IssuedCertificate{}, errors.New("cannot sign Runner certificate")
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return IssuedCertificate{}, errors.New("cannot parse signed Runner certificate")
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Raw})...)
	return IssuedCertificate{
		Certificate:          certificate,
		ChainPEM:             chain,
		CSRFingerprint:       info.CSRFingerprint,
		PublicKeyFingerprint: info.PublicKeyFingerprint,
	}, nil
}

func newRootCertificate(now time.Time, lifetime time.Duration) (generatedCertificate, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return generatedCertificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return generatedCertificate{}, err
	}
	subjectKeyID, err := publicKeyFingerprint(public)
	if err != nil {
		return generatedCertificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"YuanCI"}, CommonName: "YuanCI Runner Root CA"},
		NotBefore:             now.Add(-certificateClockBackdate),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
		SubjectKeyId:          subjectKeyID[:],
	}
	return createCertificate(template, template, private, private)
}

func newIntermediateCertificate(now time.Time, lifetime time.Duration, parent generatedCertificate) (generatedCertificate, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return generatedCertificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return generatedCertificate{}, err
	}
	subjectKeyID, err := publicKeyFingerprint(public)
	if err != nil {
		return generatedCertificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"YuanCI"}, CommonName: "YuanCI Runner Intermediate CA"},
		NotBefore:             now.Add(-certificateClockBackdate),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          subjectKeyID[:],
		AuthorityKeyId:        parent.certificate.SubjectKeyId,
	}
	return createCertificate(template, parent.certificate, private, parent.signer)
}

func newServerCertificate(now time.Time, lifetime time.Duration, dnsNames []string, ipAddresses []net.IP, parent generatedCertificate) (generatedCertificate, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return generatedCertificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return generatedCertificate{}, err
	}
	subjectKeyID, err := publicKeyFingerprint(public)
	if err != nil {
		return generatedCertificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"YuanCI"}},
		NotBefore:             now.Add(-certificateClockBackdate),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
		SubjectKeyId:          subjectKeyID[:],
		AuthorityKeyId:        parent.certificate.SubjectKeyId,
	}
	return createCertificate(template, parent.certificate, private, parent.signer)
}

func createCertificate(template, parent *x509.Certificate, subjectSigner, issuerSigner crypto.Signer) (generatedCertificate, error) {
	der, err := x509.CreateCertificate(rand.Reader, template, parent, subjectSigner.Public(), issuerSigner)
	if err != nil {
		return generatedCertificate{}, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return generatedCertificate{}, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(subjectSigner)
	if err != nil {
		return generatedCertificate{}, err
	}
	return generatedCertificate{
		certificate:    certificate,
		certificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		privateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		signer:         subjectSigner,
	}, nil
}

func normalizePKIOptions(options PKIOptions) (PKIOptions, []string, []net.IP, []string, error) {
	if strings.TrimSpace(options.OutputDir) == "" {
		return PKIOptions{}, nil, nil, nil, errors.New("PKI output directory is required")
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.RootLifetime == 0 {
		options.RootLifetime = defaultRootLifetime
	}
	if options.IntermediateLifetime == 0 {
		options.IntermediateLifetime = defaultIntermediateLife
	}
	if options.ServerLifetime == 0 {
		options.ServerLifetime = defaultServerLifetime
	}
	if options.RootLifetime < 365*24*time.Hour || options.RootLifetime > 20*365*24*time.Hour ||
		options.IntermediateLifetime < 30*24*time.Hour || options.IntermediateLifetime >= options.RootLifetime ||
		options.ServerLifetime < time.Hour || options.ServerLifetime >= options.IntermediateLifetime || options.ServerLifetime > 397*24*time.Hour {
		return PKIOptions{}, nil, nil, nil, errors.New("invalid PKI certificate lifetimes")
	}
	dnsNames, ipAddresses, names, err := validateServerNames(options.ServerNames)
	if err != nil {
		return PKIOptions{}, nil, nil, nil, err
	}
	return options, dnsNames, ipAddresses, names, nil
}

func validateServerNames(input []string) ([]string, []net.IP, []string, error) {
	if len(input) == 0 || len(input) > 32 {
		return nil, nil, nil, errors.New("one to 32 Server DNS/IP names are required")
	}
	seen := make(map[string]struct{}, len(input))
	var dnsNames []string
	var ipAddresses []net.IP
	var names []string
	for _, raw := range input {
		name := strings.TrimSpace(raw)
		if ip := net.ParseIP(name); ip != nil {
			name = ip.String()
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			ipAddresses = append(ipAddresses, ip)
			names = append(names, name)
			continue
		}
		name = strings.ToLower(name)
		if !validDNSName(name) {
			return nil, nil, nil, errors.New("invalid Server DNS/IP name")
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		dnsNames = append(dnsNames, name)
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil, nil, errors.New("one to 32 Server DNS/IP names are required")
	}
	sort.Strings(dnsNames)
	sort.Slice(ipAddresses, func(i, j int) bool { return bytes.Compare(ipAddresses[i], ipAddresses[j]) < 0 })
	sort.Strings(names)
	return dnsNames, ipAddresses, names, nil
}

func validDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 || strings.Contains(name, "*") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-') {
				return false
			}
		}
	}
	return true
}

func safeNewOutputPath(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", errors.New("invalid PKI output path")
	}
	base := filepath.Base(absolute)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", errors.New("PKI output must be a new child directory")
	}
	parent := filepath.Dir(absolute)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", errors.New("PKI output parent must be a directory")
	}
	return absolute, nil
}

func writeAtomic(root *os.Root, name string, data []byte, mode os.FileMode) error {
	token := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+"."+hex.EncodeToString(token)+".tmp")
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = root.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func syncDirectory(root *os.Root, name string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func syncHostDirectory(name string) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func randomSerial() (*big.Int, error) {
	value := make([]byte, 20)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return nil, err
	}
	value[0] &= 0x7f
	value[0] |= 0x40
	return new(big.Int).SetBytes(value), nil
}

func publicKeyFingerprint(public any) ([32]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(der), nil
}

func publicKeysEqual(left, right any) bool {
	leftDER, leftErr := x509.MarshalPKIXPublicKey(left)
	rightDER, rightErr := x509.MarshalPKIXPublicKey(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftDER, rightDER)
}

func supportedPublicKey(public any) bool {
	switch key := public.(type) {
	case ed25519.PublicKey:
		return len(key) == ed25519.PublicKeySize
	case *ecdsa.PublicKey:
		return key.Curve == elliptic.P256()
	case *rsa.PublicKey:
		bits := key.N.BitLen()
		return bits >= 2048 && bits <= 4096 && key.E >= 65537 && key.E%2 == 1
	default:
		return false
	}
}

func summarize(certificate *x509.Certificate) CertificateSummary {
	fingerprint := sha256.Sum256(certificate.Raw)
	return CertificateSummary{SHA256Fingerprint: hex.EncodeToString(fingerprint[:]), NotAfter: certificate.NotAfter.UTC()}
}
