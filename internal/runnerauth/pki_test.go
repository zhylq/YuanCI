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
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInitializePKICreatesSeparatedVerifiableBundle(t *testing.T) {
	parent := t.TempDir()
	output := filepath.Join(parent, "runner-pki")
	now := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	manifest, err := InitializePKI(PKIOptions{
		OutputDir:   output,
		ServerNames: []string{"CI.EXAMPLE.TEST", "127.0.0.1", "ci.example.test"},
		Now:         now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || !manifest.CreatedAt.Equal(now) || len(manifest.ServerNames) != 2 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	root := readCertificate(t, filepath.Join(output, "offline-root", "root-cert.pem"), 0)
	intermediate := readCertificate(t, filepath.Join(output, "server", "intermediate-cert.pem"), 0)
	server := readCertificate(t, filepath.Join(output, "server", "server-chain.pem"), 0)
	chainIntermediate := readCertificate(t, filepath.Join(output, "server", "server-chain.pem"), 1)
	if !bytes.Equal(chainIntermediate.Raw, intermediate.Raw) {
		t.Fatal("Server chain does not include issuing intermediate")
	}
	if err := root.CheckSignatureFrom(root); err != nil {
		t.Fatalf("root is not self-signed: %v", err)
	}
	if !root.IsCA || root.MaxPathLen != 1 || root.KeyUsage&(x509.KeyUsageCertSign|x509.KeyUsageCRLSign) != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Fatal("invalid root constraints")
	}
	if !intermediate.IsCA || !intermediate.MaxPathLenZero || intermediate.MaxPathLen != 0 || intermediate.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatal("invalid intermediate constraints")
	}
	if server.IsCA || len(server.ExtKeyUsage) != 1 || server.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || server.Subject.CommonName != "" {
		t.Fatal("invalid Server certificate constraints")
	}
	if server.SerialNumber.BitLen() < 128 || intermediate.SerialNumber.BitLen() < 128 || root.SerialNumber.BitLen() < 128 {
		t.Fatal("certificate serial lacks entropy")
	}

	roots := x509.NewCertPool()
	roots.AddCert(root)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(intermediate)
	for _, name := range []string{"ci.example.test", "127.0.0.1"} {
		if _, err := server.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: name, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: now}); err != nil {
			t.Fatalf("Server certificate did not verify for %s: %v", name, err)
		}
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: "wrong.example.test", CurrentTime: now}); err == nil {
		t.Fatal("Server certificate verified for wrong name")
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); err == nil {
		t.Fatal("Server certificate verified for client authentication")
	}

	assertPrivateKeyMatches(t, filepath.Join(output, "offline-root", "root-key.pem"), root)
	assertPrivateKeyMatches(t, filepath.Join(output, "server", "intermediate-key.pem"), intermediate)
	assertPrivateKeyMatches(t, filepath.Join(output, "server", "server-key.pem"), server)
	for _, name := range []string{
		filepath.Join("offline-root", "root-key.pem"),
		filepath.Join("server", "intermediate-key.pem"),
		filepath.Join("server", "server-key.pem"),
	} {
		if info, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("private key %s mode=%o", name, info.Mode().Perm())
		}
	}
	for _, name := range []string{
		filepath.Join("offline-root", "root-cert.pem"),
		filepath.Join("server", "root-cert.pem"),
		filepath.Join("server", "intermediate-cert.pem"),
		filepath.Join("server", "server-chain.pem"),
		filepath.Join("server", "manifest.json"),
	} {
		if info, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0644 {
			t.Fatalf("public PKI file %s mode=%o", name, info.Mode().Perm())
		}
	}
	for _, name := range []string{"offline-root", "server"} {
		if info, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
			t.Fatalf("PKI directory %s mode=%o", name, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(output, "server", "root-key.pem")); !os.IsNotExist(err) {
		t.Fatal("offline root key leaked into Server bundle")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(output, "server", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifestBytes, []byte("PRIVATE")) || bytes.Contains(manifestBytes, []byte("key.pem")) {
		t.Fatal("manifest contains private-key material or paths")
	}
	var diskManifest Manifest
	if err := json.Unmarshal(manifestBytes, &diskManifest); err != nil || !reflect.DeepEqual(diskManifest, manifest) {
		t.Fatalf("manifest mismatch: %v", err)
	}
	for _, pair := range []struct {
		certificate *x509.Certificate
		summary     CertificateSummary
	}{{root, manifest.Root}, {intermediate, manifest.Intermediate}, {server, manifest.Server}} {
		fingerprint := sha256.Sum256(pair.certificate.Raw)
		if pair.summary.SHA256Fingerprint != hex.EncodeToString(fingerprint[:]) || !pair.summary.NotAfter.Equal(pair.certificate.NotAfter) {
			t.Fatal("manifest certificate summary mismatch")
		}
	}
}

func TestInitializePKINeverOverwritesAndCleansValidationFailure(t *testing.T) {
	parent := t.TempDir()
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing, "keep")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializePKI(PKIOptions{OutputDir: existing, ServerNames: []string{"server"}}); err == nil {
		t.Fatal("existing PKI directory was accepted")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "unchanged" {
		t.Fatal("existing output was changed")
	}

	for index, invalidName := range []string{"", "https://ci.example.test", "ci.example.test:443", "*.example.test", "bad_name"} {
		output := filepath.Join(parent, "invalid-"+string(rune('a'+index)))
		if _, err := InitializePKI(PKIOptions{OutputDir: output, ServerNames: []string{invalidName}}); err == nil {
			t.Fatalf("invalid Server name %q accepted", invalidName)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("validation failure left partial output %s", output)
		}
	}
	missingParent := filepath.Join(parent, "missing", "bundle")
	if _, err := InitializePKI(PKIOptions{OutputDir: missingParent, ServerNames: []string{"server"}}); err == nil {
		t.Fatal("missing parent accepted")
	}
	if _, err := os.Stat(missingParent); !os.IsNotExist(err) {
		t.Fatal("missing-parent failure left partial output")
	}
}

func TestValidateRunnerCSRAlgorithmsAndInputPolicy(t *testing.T) {
	_, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, signer := range map[string]crypto.Signer{"ed25519": edPrivate, "p256": p256, "rsa2048": rsa2048} {
		t.Run(name, func(t *testing.T) {
			csr := makeCSR(t, signer, &x509.CertificateRequest{})
			info, err := ValidateRunnerCSR(csr)
			if err != nil || info.Request == nil || info.CSRFingerprint == ([32]byte{}) || info.PublicKeyFingerprint == ([32]byte{}) {
				t.Fatalf("valid CSR rejected: %v", err)
			}
		})
	}

	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsa1024, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	valid := makeCSR(t, edPrivate, &x509.CertificateRequest{})
	duplicate := append(append([]byte(nil), valid...), valid...)
	badSignatureBlock, _ := pem.Decode(valid)
	badSignatureBlock.Bytes[len(badSignatureBlock.Bytes)-1] ^= 0xff
	badSignature := pem.EncodeToMemory(badSignatureBlock)
	invalid := map[string][]byte{
		"empty":           nil,
		"malformed":       []byte("not a csr"),
		"trailing":        append(append([]byte(nil), valid...), []byte("unexpected")...),
		"multiple blocks": duplicate,
		"oversized":       bytes.Repeat([]byte("x"), MaxCSRBytes+1),
		"bad signature":   badSignature,
		"subject":         makeCSR(t, edPrivate, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "not-authority"}}),
		"dns san":         makeCSR(t, edPrivate, &x509.CertificateRequest{DNSNames: []string{"runner.example.test"}}),
		"extension": makeCSR(t, edPrivate, &x509.CertificateRequest{ExtraExtensions: []pkix.Extension{{
			Id: []int{1, 2, 3, 4}, Value: []byte{1},
		}}}),
		"p384":    makeCSR(t, p384, &x509.CertificateRequest{}),
		"rsa1024": makeCSR(t, rsa1024, &x509.CertificateRequest{}),
	}
	for name, csr := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateRunnerCSR(csr); !errors.Is(err, ErrInvalidCSR) {
				t.Fatalf("invalid CSR accepted or unstable error: %v", err)
			}
		})
	}
	if _, err := ValidateRunnerCSR(append(valid, []byte(" \r\n\t")...)); err != nil {
		t.Fatal("harmless trailing whitespace rejected")
	}
}

func TestSignRunnerCertificateBindsClientIdentity(t *testing.T) {
	output := filepath.Join(t.TempDir(), "pki")
	now := time.Date(2026, 9, 1, 5, 0, 0, 0, time.UTC)
	if _, err := InitializePKI(PKIOptions{OutputDir: output, ServerNames: []string{"server"}, Now: now}); err != nil {
		t.Fatal(err)
	}
	root := readCertificate(t, filepath.Join(output, "offline-root", "root-cert.pem"), 0)
	intermediate := readCertificate(t, filepath.Join(output, "server", "intermediate-cert.pem"), 0)
	issuerKey := readSigner(t, filepath.Join(output, "server", "intermediate-key.pem"))
	_, runnerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr := makeCSR(t, runnerKey, &x509.CertificateRequest{})
	runnerID := uuid.New()
	issued, err := SignRunnerCertificate(csr, runnerID, intermediate, issuerKey, now, DefaultRunnerLifetime)
	if err != nil {
		t.Fatal(err)
	}
	certificate := issued.Certificate
	if certificate.IsCA || certificate.Subject.String() != "" || len(certificate.URIs) != 1 || certificate.URIs[0].String() != "yuanci://runner/"+runnerID.String() || certificate.SerialNumber.BitLen() < 128 {
		t.Fatalf("invalid Runner identity certificate: %+v", certificate)
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || certificate.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Fatal("invalid Runner certificate usage")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(intermediate)
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: now}); err == nil {
		t.Fatal("Runner certificate verified for Server authentication")
	}
	if bytes.Contains(issued.ChainPEM, []byte("PRIVATE KEY")) {
		t.Fatal("issued chain contains private key")
	}
	if _, err := SignRunnerCertificate(csr, uuid.Nil, intermediate, issuerKey, now, time.Hour); err == nil {
		t.Fatal("nil Runner identity accepted")
	}
	if _, err := SignRunnerCertificate(csr, runnerID, intermediate, issuerKey, now, DefaultRunnerLifetime+time.Second); err == nil {
		t.Fatal("overlong Runner certificate accepted")
	}
	_, wrongIssuerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SignRunnerCertificate(csr, runnerID, intermediate, wrongIssuerKey, now, time.Hour); err == nil {
		t.Fatal("issuer certificate/key mismatch accepted")
	}
	if _, err := SignRunnerCertificate(csr, runnerID, intermediate, issuerKey, intermediate.NotAfter, time.Hour); err == nil {
		t.Fatal("expired issuer accepted")
	}
}

func FuzzValidateRunnerCSR(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("-----BEGIN CERTIFICATE REQUEST-----\ninvalid\n-----END CERTIFICATE REQUEST-----\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = ValidateRunnerCSR(input)
	})
}

func makeCSR(t *testing.T, signer crypto.Signer, template *x509.CertificateRequest) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, template, signer)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func readCertificate(t *testing.T, path string, index int) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for current := 0; ; current++ {
		block, rest := pem.Decode(data)
		if block == nil {
			t.Fatalf("certificate %d missing from %s", index, path)
		}
		if current == index {
			certificate, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			return certificate
		}
		data = rest
	}
}

func readSigner(t *testing.T, path string) crypto.Signer {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("invalid private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		t.Fatal("private key is not a signer")
	}
	return signer
}

func assertPrivateKeyMatches(t *testing.T, path string, certificate *x509.Certificate) {
	t.Helper()
	signer := readSigner(t, path)
	if !publicKeysEqual(signer.Public(), certificate.PublicKey) {
		t.Fatalf("private key %s does not match certificate", path)
	}
}
