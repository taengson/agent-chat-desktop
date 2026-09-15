package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// BenchmarkSyncV2TLSCertificateRequest identifies the two user-chosen files
// created for a public TLS certificate application. The private-key contents
// are intentionally never returned to the frontend.
type BenchmarkSyncV2TLSCertificateRequest struct {
	Domain                 string `json:"domain"`
	PrivateKeyPath         string `json:"privateKeyPath"`
	CertificateRequestPath string `json:"certificateRequestPath"`
}

// GenerateBenchmarkSyncV2TLSCertificateRequest creates a TLS private key and
// CSR for the supplied public HTTPS domain. A public or organisation CA must
// sign the CSR before the device can accept real remote connections. The
// caller selects new output files; existing files are never overwritten.
func GenerateBenchmarkSyncV2TLSCertificateRequest(publicHTTPSURL, privateKeyPath, certificateRequestPath string) (BenchmarkSyncV2TLSCertificateRequest, error) {
	return generateBenchmarkSyncV2TLSCertificateRequest(publicHTTPSURL, privateKeyPath, certificateRequestPath)
}

func generateBenchmarkSyncV2TLSCertificateRequest(publicHTTPSURL, privateKeyPath, certificateRequestPath string) (BenchmarkSyncV2TLSCertificateRequest, error) {
	publicHTTPSURL, err := normalizeBenchmarkSyncV2PublicHTTPSURL(publicHTTPSURL)
	if err != nil {
		return BenchmarkSyncV2TLSCertificateRequest{}, err
	}
	endpoint, err := url.Parse(publicHTTPSURL)
	if err != nil || endpoint.Hostname() == "" {
		return BenchmarkSyncV2TLSCertificateRequest{}, errors.New("외부 HTTPS 주소의 도메인을 확인할 수 없습니다")
	}
	privateKeyPath, err = normalizeBenchmarkSyncV2NewTLSOutputPath(privateKeyPath, "개인키", ".key", ".pem")
	if err != nil {
		return BenchmarkSyncV2TLSCertificateRequest{}, err
	}
	certificateRequestPath, err = normalizeBenchmarkSyncV2NewTLSOutputPath(certificateRequestPath, "인증서 요청", ".csr", ".pem")
	if err != nil {
		return BenchmarkSyncV2TLSCertificateRequest{}, err
	}
	if privateKeyPath == certificateRequestPath {
		return BenchmarkSyncV2TLSCertificateRequest{}, errors.New("개인키와 인증서 요청은 서로 다른 파일에 저장해 주세요")
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return BenchmarkSyncV2TLSCertificateRequest{}, fmt.Errorf("TLS 개인키를 만들 수 없습니다: %w", err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return BenchmarkSyncV2TLSCertificateRequest{}, fmt.Errorf("TLS 개인키를 저장할 수 없습니다: %w", err)
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		DNSNames: []string{endpoint.Hostname()},
	}, privateKey)
	if err != nil {
		return BenchmarkSyncV2TLSCertificateRequest{}, fmt.Errorf("TLS 인증서 요청을 만들 수 없습니다: %w", err)
	}

	if err := writeBenchmarkSyncV2NewSecretFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})); err != nil {
		return BenchmarkSyncV2TLSCertificateRequest{}, err
	}
	if err := writeBenchmarkSyncV2NewSecretFile(certificateRequestPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER})); err != nil {
		// This file was created by this function moments ago, so it is safe to
		// remove it if the paired CSR write cannot complete.
		_ = os.Remove(privateKeyPath)
		return BenchmarkSyncV2TLSCertificateRequest{}, err
	}
	return BenchmarkSyncV2TLSCertificateRequest{
		Domain:                 endpoint.Hostname(),
		PrivateKeyPath:         privateKeyPath,
		CertificateRequestPath: certificateRequestPath,
	}, nil
}

func normalizeBenchmarkSyncV2NewTLSOutputPath(value, label string, allowedExtensions ...string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return "", fmt.Errorf("TLS %s 파일을 선택해 주세요", label)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("TLS %s 파일은 절대 경로여야 합니다", label)
	}
	path = filepath.Clean(path)
	extension := strings.ToLower(filepath.Ext(path))
	for _, allowed := range allowedExtensions {
		if extension == allowed {
			goto extensionOK
		}
	}
	return "", fmt.Errorf("TLS %s 파일은 %s 형식으로 저장해 주세요", label, strings.Join(allowedExtensions, " 또는 "))

extensionOK:
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("TLS %s 파일을 저장할 폴더를 찾을 수 없습니다", label)
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("TLS %s 파일이 이미 있습니다. 새 파일 이름을 선택해 주세요", label)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("TLS %s 파일을 준비할 수 없습니다", label)
	}
	return path, nil
}

func writeBenchmarkSyncV2NewSecretFile(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("선택한 파일이 이미 있습니다. 기존 파일은 덮어쓰지 않았습니다")
	}
	if err != nil {
		return errors.New("선택한 파일을 만들 수 없습니다")
	}
	defer file.Close()
	if _, err := file.Write(contents); err != nil {
		return errors.New("TLS 파일을 저장할 수 없습니다")
	}
	return nil
}
