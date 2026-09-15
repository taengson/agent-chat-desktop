package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const benchmarkSyncV2HealthPath = "/agent-chat/benchmark-sync/v2/health"

// benchmarkSyncV2DirectTLSEndpoint owns an in-process TLS listener. It is
// deliberately not a reverse-proxy adapter: the desktop app loads its own
// certificate and private-key files and terminates TLS itself.
type benchmarkSyncV2DirectTLSEndpoint struct {
	listener net.Listener
	server   *http.Server
}

// ConfigureDirectTLS validates a public HTTPS origin, its matching
// certificate/key pair, and the local listen address. After storing only the
// file paths (never the key material), it replaces any current v2 listener.
func (s *benchmarkSyncV2Store) ConfigureDirectTLS(publicHTTPSURL, certificatePath, privateKeyPath, listenAddress string) (BenchmarkSyncV2State, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	config, err := newBenchmarkSyncV2DirectTLSConfig(publicHTTPSURL, certificatePath, privateKeyPath, listenAddress)
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}

	s.mu.Lock()
	if err := s.ensureLoadedLocked(); err != nil {
		s.mu.Unlock()
		return BenchmarkSyncV2State{}, err
	}
	s.state.PublicHTTPSURL = config.publicHTTPSURL
	s.state.TLSCertificatePath = config.certificatePath
	s.state.TLSPrivateKeyPath = config.privateKeyPath
	s.state.ListenAddress = config.listenAddress
	s.appendLogLocked("direct-tls-configured", "completed", "직접 TLS 수신 설정을 저장했습니다")
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return BenchmarkSyncV2State{}, err
	}
	s.mu.Unlock()

	if err := s.restartEndpoint(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.stateForIdentity(keySet.publicIdentity)
}

// StartConfiguredEndpoint is called at application startup and after direct
// TLS configuration changes. A listener error is recorded locally but must
// not prevent chat and benchmark features from opening.
func (s *benchmarkSyncV2Store) StartConfiguredEndpoint() error {
	s.mu.Lock()
	if err := s.ensureLoadedLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.endpoint != nil {
		s.mu.Unlock()
		return nil
	}
	if !s.hasDirectTLSConfigurationLocked() {
		s.mu.Unlock()
		return nil
	}
	state := s.state
	s.mu.Unlock()

	config, err := newBenchmarkSyncV2DirectTLSConfig(state.PublicHTTPSURL, state.TLSCertificatePath, state.TLSPrivateKeyPath, state.ListenAddress)
	if err != nil {
		s.recordEndpointFailure(err)
		return err
	}
	endpoint, err := newBenchmarkSyncV2DirectTLSEndpoint(config, s)
	if err != nil {
		s.recordEndpointFailure(err)
		return err
	}

	s.mu.Lock()
	if s.endpoint != nil {
		s.mu.Unlock()
		_ = endpoint.close()
		return nil
	}
	s.endpoint = endpoint
	s.appendLogLocked("direct-tls-listener-started", "completed", "v2 HTTPS 수신 endpoint를 시작했습니다")
	if err := s.saveLocked(); err != nil {
		s.endpoint = nil
		s.mu.Unlock()
		_ = endpoint.close()
		return err
	}
	s.mu.Unlock()

	go s.serveEndpoint(endpoint)
	return nil
}

func (s *benchmarkSyncV2Store) StopEndpoint() (BenchmarkSyncV2State, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	if err := s.stopEndpoint(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.stateForIdentity(keySet.publicIdentity)
}

func (s *benchmarkSyncV2Store) restartEndpoint() error {
	if err := s.stopEndpoint(); err != nil {
		return err
	}
	return s.StartConfiguredEndpoint()
}

func (s *benchmarkSyncV2Store) stopEndpoint() error {
	s.mu.Lock()
	endpoint := s.endpoint
	if endpoint == nil {
		s.mu.Unlock()
		return nil
	}
	s.endpoint = nil
	if s.loaded {
		s.appendLogLocked("direct-tls-listener-stopped", "completed", "v2 HTTPS 수신 endpoint를 중지했습니다")
		if err := s.saveLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.mu.Unlock()
	return endpoint.close()
}

func (s *benchmarkSyncV2Store) serveEndpoint(endpoint *benchmarkSyncV2DirectTLSEndpoint) {
	err := endpoint.server.Serve(endpoint.listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	s.mu.Lock()
	if s.endpoint == endpoint {
		s.endpoint = nil
		s.appendLogLocked("direct-tls-listener-stopped", "failed", "v2 HTTPS 수신 endpoint가 중지되었습니다")
		_ = s.saveLocked()
	}
	s.mu.Unlock()
}

func (s *benchmarkSyncV2Store) recordEndpointFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return
	}
	s.appendLogLocked("direct-tls-listener-start", "failed", "v2 HTTPS 수신 endpoint를 시작하지 못했습니다")
	_ = s.saveLocked()
}

func (s *benchmarkSyncV2Store) stateForIdentity(identity BenchmarkSyncV2Identity) (BenchmarkSyncV2State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.publicStateLocked(identity), nil
}

type benchmarkSyncV2DirectTLSConfig struct {
	publicHTTPSURL  string
	certificatePath string
	privateKeyPath  string
	listenAddress   string
	certificate     tls.Certificate
}

func newBenchmarkSyncV2DirectTLSConfig(publicHTTPSURL, certificatePath, privateKeyPath, listenAddress string) (benchmarkSyncV2DirectTLSConfig, error) {
	publicHTTPSURL, err := normalizeBenchmarkSyncV2PublicHTTPSURL(publicHTTPSURL)
	if err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, err
	}
	certificatePath, err = normalizeBenchmarkSyncV2TLSFilePath(certificatePath, "인증서")
	if err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, err
	}
	privateKeyPath, err = normalizeBenchmarkSyncV2TLSFilePath(privateKeyPath, "개인키")
	if err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, err
	}
	listenAddress, err = normalizeBenchmarkSyncV2ListenAddress(listenAddress)
	if err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, err
	}
	certificate, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, errors.New("TLS 인증서와 개인키를 함께 읽을 수 없습니다")
	}
	if len(certificate.Certificate) == 0 {
		return benchmarkSyncV2DirectTLSConfig{}, errors.New("TLS 인증서가 비어 있습니다")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, errors.New("TLS 인증서 형식이 올바르지 않습니다")
	}
	endpoint, err := url.Parse(publicHTTPSURL)
	if err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, errors.New("외부 HTTPS 주소를 해석할 수 없습니다")
	}
	if err := leaf.VerifyHostname(endpoint.Hostname()); err != nil {
		return benchmarkSyncV2DirectTLSConfig{}, errors.New("TLS 인증서의 도메인이 외부 HTTPS 주소와 일치하지 않습니다")
	}
	return benchmarkSyncV2DirectTLSConfig{
		publicHTTPSURL:  publicHTTPSURL,
		certificatePath: certificatePath,
		privateKeyPath:  privateKeyPath,
		listenAddress:   listenAddress,
		certificate:     certificate,
	}, nil
}

func normalizeBenchmarkSyncV2DirectTLSPaths(state *benchmarkSyncV2PersistentState) error {
	values := []string{state.TLSCertificatePath, state.TLSPrivateKeyPath, state.ListenAddress}
	configured := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	if configured != len(values) || state.PublicHTTPSURL == "" {
		return errors.New("저장된 직접 TLS 수신 설정이 완전하지 않습니다")
	}
	certificatePath, err := normalizeBenchmarkSyncV2PersistedTLSFilePath(state.TLSCertificatePath, "인증서")
	if err != nil {
		return err
	}
	privateKeyPath, err := normalizeBenchmarkSyncV2PersistedTLSFilePath(state.TLSPrivateKeyPath, "개인키")
	if err != nil {
		return err
	}
	listenAddress, err := normalizeBenchmarkSyncV2ListenAddress(state.ListenAddress)
	if err != nil {
		return err
	}
	state.TLSCertificatePath = certificatePath
	state.TLSPrivateKeyPath = privateKeyPath
	state.ListenAddress = listenAddress
	return nil
}

func normalizeBenchmarkSyncV2TLSFilePath(value, label string) (string, error) {
	path, err := normalizeBenchmarkSyncV2PersistedTLSFilePath(value, label)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("TLS %s 파일을 찾을 수 없습니다", label)
	}
	return path, nil
}

func normalizeBenchmarkSyncV2PersistedTLSFilePath(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("TLS %s 파일 경로를 입력해 주세요", label)
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("TLS %s 파일 경로가 올바르지 않습니다", label)
	}
	return path, nil
}

func normalizeBenchmarkSyncV2ListenAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("v2 HTTPS 수신 주소를 입력해 주세요. 예: :443")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return "", errors.New("v2 HTTPS 수신 주소는 포트를 포함해야 합니다. 예: :443")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("v2 HTTPS 수신 포트가 올바르지 않습니다")
	}
	if host != "" && host != "0.0.0.0" && host != "::" && net.ParseIP(host) == nil {
		return "", errors.New("v2 HTTPS 수신 주소에는 로컬 IP 또는 모든 인터페이스 주소만 사용할 수 있습니다")
	}
	return value, nil
}

func (s *benchmarkSyncV2Store) hasDirectTLSConfigurationLocked() bool {
	return s.state.PublicHTTPSURL != "" && s.state.TLSCertificatePath != "" && s.state.TLSPrivateKeyPath != "" && s.state.ListenAddress != ""
}

func newBenchmarkSyncV2DirectTLSEndpoint(config benchmarkSyncV2DirectTLSConfig, store *benchmarkSyncV2Store) (*benchmarkSyncV2DirectTLSEndpoint, error) {
	listener, err := tls.Listen("tcp", config.listenAddress, &tls.Config{
		Certificates: []tls.Certificate{config.certificate},
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		return nil, fmt.Errorf("v2 HTTPS 수신 주소를 열 수 없습니다: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(benchmarkSyncV2HealthPath, benchmarkSyncV2HealthHandler)
	if store != nil {
		mux.HandleFunc(benchmarkSyncV2PairPath, store.handlePairingRequest)
		mux.HandleFunc(benchmarkSyncV2PairStatusPath, store.handlePairingStatus)
	}
	server := &http.Server{
		Handler:           secureBenchmarkSyncV2Headers(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	return &benchmarkSyncV2DirectTLSEndpoint{listener: listener, server: server}, nil
}

func (endpoint *benchmarkSyncV2DirectTLSEndpoint) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return endpoint.server.Shutdown(ctx)
}

func benchmarkSyncV2HealthHandler(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"protocolVersion": 2,
		"status":          "pairing-not-enabled",
	})
}

func secureBenchmarkSyncV2Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}
