package runnergrpc

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"runtime"
)

type PKIFiles struct {
	ServerCertificate string
	ServerKey         string
	ClientCA          string
	IssuerCertificate string
	IssuerKey         string
}

type PKI struct {
	TLSConfig *tls.Config
	Issuer    *x509.Certificate
	IssuerKey crypto.Signer
	RootPEM   []byte
}

func LoadPKI(files PKIFiles) (PKI, error) {
	serverCertificate, err := readRegular(files.ServerCertificate, 64<<10, false)
	if err != nil {
		return PKI{}, errors.New("invalid Runner Server certificate file")
	}
	serverKey, err := readRegular(files.ServerKey, 32<<10, true)
	if err != nil {
		return PKI{}, errors.New("invalid Runner Server key file")
	}
	serverPair, err := tls.X509KeyPair(serverCertificate, serverKey)
	clear(serverKey)
	if err != nil {
		return PKI{}, errors.New("invalid Runner Server certificate pair")
	}
	rootPEM, err := readRegular(files.ClientCA, 32<<10, false)
	if err != nil {
		return PKI{}, errors.New("invalid Runner client CA file")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		return PKI{}, errors.New("invalid Runner client CA certificate")
	}
	issuerPEM, err := readRegular(files.IssuerCertificate, 32<<10, false)
	if err != nil {
		return PKI{}, errors.New("invalid Runner issuer certificate file")
	}
	issuer, err := parseOneCertificate(issuerPEM)
	if err != nil || !issuer.IsCA || issuer.KeyUsage&x509.KeyUsageCertSign == 0 {
		return PKI{}, errors.New("invalid Runner issuer certificate")
	}
	issuerKeyPEM, err := readRegular(files.IssuerKey, 32<<10, true)
	if err != nil {
		return PKI{}, errors.New("invalid Runner issuer key file")
	}
	issuerKey, err := parseSigner(issuerKeyPEM)
	clear(issuerKeyPEM)
	if err != nil || !publicKeysEqual(issuer.PublicKey, issuerKey.Public()) {
		return PKI{}, errors.New("invalid Runner issuer certificate pair")
	}
	return PKI{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverPair},
			ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: roots},
		Issuer: issuer, IssuerKey: issuerKey, RootPEM: append([]byte(nil), rootPEM...),
	}, nil
}

func readRegular(path string, limit int64, private bool) ([]byte, error) {
	if path == "" {
		return nil, errors.New("path missing")
	}
	linkInfo, err := os.Lstat(path)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("invalid file")
	}
	if private && runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private file permissions are too broad")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("file too large")
	}
	return data, nil
}

func parseOneCertificate(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseSigner(data []byte) (crypto.Signer, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("key is not a signer")
	}
	return signer, nil
}

func publicKeysEqual(left, right any) bool {
	leftDER, leftErr := x509.MarshalPKIXPublicKey(left)
	rightDER, rightErr := x509.MarshalPKIXPublicKey(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftDER, rightDER)
}
