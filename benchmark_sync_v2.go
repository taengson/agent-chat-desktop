package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	benchmarkSyncV2FileName    = "benchmark-sync-v2.json"
	benchmarkSyncV2MaxLogCount = 200
)

// BenchmarkSyncV2Log is a local audit record. It deliberately stores neither
// pairing secrets, private keys, plaintext benchmark payloads, nor ciphertext.
type BenchmarkSyncV2Log struct {
	ID         string `json:"id"`
	OccurredAt string `json:"occurredAt"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// BenchmarkSyncV2State is the complete public state for the replacement sync
// feature. Unlike v1, it has no LAN addresses, one-time display code, or
// bearer-token-derived peer state.
type BenchmarkSyncV2State struct {
	DeviceName         string                           `json:"deviceName"`
	Identity           BenchmarkSyncV2Identity          `json:"identity"`
	PublicHTTPSURL     string                           `json:"publicHTTPSURL,omitempty"`
	TLSCertificatePath string                           `json:"tlsCertificatePath,omitempty"`
	TLSPrivateKeyPath  string                           `json:"tlsPrivateKeyPath,omitempty"`
	ListenAddress      string                           `json:"listenAddress,omitempty"`
	EndpointStatus     string                           `json:"endpointStatus"`
	Peers              []BenchmarkSyncV2Peer            `json:"peers"`
	PendingPairings    []BenchmarkSyncV2PendingPairing  `json:"pendingPairings"`
	OutgoingPairings   []BenchmarkSyncV2OutgoingPairing `json:"outgoingPairings"`
	Logs               []BenchmarkSyncV2Log             `json:"logs"`
}

// BenchmarkSyncV2Peer is public trust information for a paired device. Its
// E2EE session secrets stay in the OS secret store once encryption is enabled.
type BenchmarkSyncV2Peer struct {
	DeviceName               string `json:"deviceName"`
	PublicHTTPSURL           string `json:"publicHTTPSURL,omitempty"`
	SigningKeyID             string `json:"signingKeyID"`
	SigningPublicKeyPEM      string `json:"signingPublicKeyPEM"`
	SigningFingerprint       string `json:"signingFingerprint"`
	KeyAgreementKeyID        string `json:"keyAgreementKeyID"`
	KeyAgreementPublicKeyPEM string `json:"keyAgreementPublicKeyPEM"`
	KeyAgreementFingerprint  string `json:"keyAgreementFingerprint"`
	TrustedAt                string `json:"trustedAt"`
}

// BenchmarkSyncV2PendingPairing is a host-side request awaiting a fingerprint
// comparison and explicit user approval.
type BenchmarkSyncV2PendingPairing struct {
	RequestID string              `json:"requestID"`
	Requester BenchmarkSyncV2Peer `json:"requester"`
	ExpiresAt string              `json:"expiresAt"`
}

// BenchmarkSyncV2OutgoingPairing omits the invitation secret deliberately.
// The secret exists only in process memory until pairing is resolved.
type BenchmarkSyncV2OutgoingPairing struct {
	RequestID string              `json:"requestID"`
	Host      BenchmarkSyncV2Peer `json:"host"`
	Status    string              `json:"status"`
	ExpiresAt string              `json:"expiresAt"`
}

// BenchmarkSyncV2Invitation is returned only when a user creates an
// invitation. PairingSecret is never copied into state, audit logs, or JSON.
type BenchmarkSyncV2Invitation struct {
	PublicHTTPSURL string                  `json:"publicHTTPSURL"`
	PairingSecret  string                  `json:"pairingSecret"`
	ExpiresAt      string                  `json:"expiresAt"`
	Host           BenchmarkSyncV2Identity `json:"host"`
}

type benchmarkSyncV2PersistentState struct {
	DeviceName         string                `json:"deviceName"`
	PublicHTTPSURL     string                `json:"publicHTTPSURL,omitempty"`
	TLSCertificatePath string                `json:"tlsCertificatePath,omitempty"`
	TLSPrivateKeyPath  string                `json:"tlsPrivateKeyPath,omitempty"`
	ListenAddress      string                `json:"listenAddress,omitempty"`
	Peers              []BenchmarkSyncV2Peer `json:"peers,omitempty"`
	Logs               []BenchmarkSyncV2Log  `json:"logs"`
}

// benchmarkSyncV2Store is intentionally independent from the old LAN sync
// implementation. v2 pairing, TLS transport, and direct E2EE build on this
// persistent public state and the separate OS-backed identity store.
type benchmarkSyncV2Store struct {
	root       string
	identities *benchmarkSyncV2IdentityStore

	mu               sync.Mutex
	loaded           bool
	state            benchmarkSyncV2PersistentState
	endpoint         *benchmarkSyncV2DirectTLSEndpoint
	invitations      map[string]benchmarkSyncV2InvitationState
	pendingPairings  map[string]benchmarkSyncV2PendingPairingState
	outgoingPairings map[string]benchmarkSyncV2OutgoingPairingState
	httpClient       *http.Client
}

func newBenchmarkSyncV2Store(root string) *benchmarkSyncV2Store {
	return &benchmarkSyncV2Store{
		root:             root,
		identities:       newBenchmarkSyncV2IdentityStore(benchmarkSyncV2SystemSecretStore{}),
		httpClient:       newBenchmarkSyncV2HTTPClient(),
		invitations:      make(map[string]benchmarkSyncV2InvitationState),
		pendingPairings:  make(map[string]benchmarkSyncV2PendingPairingState),
		outgoingPairings: make(map[string]benchmarkSyncV2OutgoingPairingState),
	}
}

func (s *benchmarkSyncV2Store) Identity() (BenchmarkSyncV2Identity, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2Identity{}, err
	}
	return keySet.publicIdentity, nil
}

func (s *benchmarkSyncV2Store) State() (BenchmarkSyncV2State, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.publicStateLocked(keySet.publicIdentity), nil
}

func (s *benchmarkSyncV2Store) UpdateDeviceName(name string) (BenchmarkSyncV2State, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	name, err = normalizeBenchmarkSyncV2DeviceName(name)
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	s.state.DeviceName = name
	s.appendLogLocked("device-name-updated", "completed", "이 장치 이름을 변경했습니다")
	if err := s.saveLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.publicStateLocked(keySet.publicIdentity), nil
}

func (s *benchmarkSyncV2Store) ClearLogs() (BenchmarkSyncV2State, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	s.state.Logs = nil
	if err := s.saveLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.publicStateLocked(keySet.publicIdentity), nil
}

func (s *benchmarkSyncV2Store) SignRecord(benchmark ModelBenchmark) (ModelBenchmark, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return ModelBenchmark{}, err
	}
	return signBenchmarkSyncV2Record(keySet, benchmark, s.identities.now())
}

func (s *benchmarkSyncV2Store) VerifyRecord(benchmark ModelBenchmark, peerSigningPublicKeyPEM string) error {
	return verifyBenchmarkSyncV2RecordProof(benchmark, peerSigningPublicKeyPEM)
}

func (s *benchmarkSyncV2Store) Close() error {
	return s.stopEndpoint()
}

func (s *benchmarkSyncV2Store) ensureLoadedLocked() error {
	if s.loaded {
		return nil
	}
	path, err := s.filePath()
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		name, nameErr := defaultBenchmarkSyncV2DeviceName()
		if nameErr != nil {
			return nameErr
		}
		s.state = benchmarkSyncV2PersistentState{DeviceName: name}
		s.loaded = true
		return s.saveLocked()
	}
	if err != nil {
		return fmt.Errorf("v2 동기화 정보를 읽을 수 없습니다: %w", err)
	}
	if err := json.Unmarshal(contents, &s.state); err != nil {
		return errors.New("v2 동기화 정보 형식이 올바르지 않습니다")
	}
	name, err := normalizeBenchmarkSyncV2DeviceName(s.state.DeviceName)
	if err != nil {
		return errors.New("v2 동기화 장치 이름이 올바르지 않습니다")
	}
	s.state.DeviceName = name
	if s.state.PublicHTTPSURL != "" {
		endpoint, endpointErr := normalizeBenchmarkSyncV2PublicHTTPSURL(s.state.PublicHTTPSURL)
		if endpointErr != nil {
			return errors.New("저장된 외부 HTTPS 주소가 올바르지 않습니다")
		}
		s.state.PublicHTTPSURL = endpoint
	}
	if err := normalizeBenchmarkSyncV2DirectTLSPaths(&s.state); err != nil {
		return err
	}
	s.loaded = true
	return nil
}

func (s *benchmarkSyncV2Store) publicStateLocked(identity BenchmarkSyncV2Identity) BenchmarkSyncV2State {
	logs := append([]BenchmarkSyncV2Log(nil), s.state.Logs...)
	endpointStatus := "not-configured"
	if s.state.PublicHTTPSURL != "" {
		endpointStatus = "needs-tls-configuration"
	}
	if s.hasDirectTLSConfigurationLocked() {
		endpointStatus = "configured"
	}
	if s.endpoint != nil {
		endpointStatus = "listening"
	}
	return BenchmarkSyncV2State{
		DeviceName:         s.state.DeviceName,
		Identity:           identity,
		PublicHTTPSURL:     s.state.PublicHTTPSURL,
		TLSCertificatePath: s.state.TLSCertificatePath,
		TLSPrivateKeyPath:  s.state.TLSPrivateKeyPath,
		ListenAddress:      s.state.ListenAddress,
		EndpointStatus:     endpointStatus,
		Peers:              append([]BenchmarkSyncV2Peer(nil), s.state.Peers...),
		PendingPairings:    s.publicPendingPairingsLocked(),
		OutgoingPairings:   s.publicOutgoingPairingsLocked(),
		Logs:               logs,
	}
}

func (s *benchmarkSyncV2Store) appendLogLocked(event, status, message string) {
	identifier, err := newBenchmarkSyncV2Nonce()
	if err != nil {
		return
	}
	s.state.Logs = append([]BenchmarkSyncV2Log{{
		ID:         identifier,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		Event:      event,
		Status:     status,
		Message:    message,
	}}, s.state.Logs...)
	if len(s.state.Logs) > benchmarkSyncV2MaxLogCount {
		s.state.Logs = s.state.Logs[:benchmarkSyncV2MaxLogCount]
	}
}

func (s *benchmarkSyncV2Store) filePath() (string, error) {
	root, err := applicationDataDirectory(s.root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("v2 동기화 저장 폴더를 만들 수 없습니다: %w", err)
	}
	return filepath.Join(root, benchmarkSyncV2FileName), nil
}

func (s *benchmarkSyncV2Store) saveLocked() error {
	path, err := s.filePath()
	if err != nil {
		return err
	}
	contents, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("v2 동기화 정보를 저장할 수 없습니다: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".benchmark-sync-v2-*")
	if err != nil {
		return fmt.Errorf("v2 동기화 임시 파일을 만들 수 없습니다: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("v2 동기화 정보 권한을 설정할 수 없습니다: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("v2 동기화 정보를 저장할 수 없습니다: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("v2 동기화 정보를 저장할 수 없습니다: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("v2 동기화 정보를 저장할 수 없습니다: %w", err)
	}
	return nil
}

func defaultBenchmarkSyncV2DeviceName() (string, error) {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		name = "Agent Chat"
	}
	return normalizeBenchmarkSyncV2DeviceName(name)
}

func normalizeBenchmarkSyncV2DeviceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 || strings.ContainsAny(name, "\r\n") {
		return "", errors.New("장치 이름은 1자 이상 80자 이하로 입력해 주세요")
	}
	return name, nil
}

func normalizeBenchmarkSyncV2PublicHTTPSURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return "", errors.New("공개 CA 또는 조직 CA 인증서를 사용하는 HTTPS 주소를 입력해 주세요")
	}
	host := parsed.Hostname()
	if host == "" || net.ParseIP(host) != nil || strings.EqualFold(host, "localhost") {
		return "", errors.New("v2 외부 주소에는 도메인 이름을 사용해 주세요")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}
