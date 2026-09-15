package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type benchmarkSyncV2MemorySecretStore struct {
	mu      sync.Mutex
	entries map[string]string
}

func TestBenchmarkSyncV2DirectTLSEndpointServesOnlyOverMatchingTLSCertificate(t *testing.T) {
	certificatePath, privateKeyPath, certificatePEM := writeBenchmarkSyncV2TestCertificate(t, "sync.example.test")
	listenAddress := reserveBenchmarkSyncV2TestListenAddress(t)
	config, err := newBenchmarkSyncV2DirectTLSConfig(
		"https://sync.example.test",
		certificatePath,
		privateKeyPath,
		listenAddress,
	)
	if err != nil {
		t.Fatalf("newBenchmarkSyncV2DirectTLSConfig() error = %v", err)
	}
	endpoint, err := newBenchmarkSyncV2DirectTLSEndpoint(config, nil)
	if err != nil {
		t.Fatalf("newBenchmarkSyncV2DirectTLSEndpoint() error = %v", err)
	}
	defer endpoint.close()
	go func() { _ = endpoint.server.Serve(endpoint.listener) }()

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("AppendCertsFromPEM() did not add the test certificate")
	}
	connection, err := tls.Dial("tcp", endpoint.listener.Addr().String(), &tls.Config{
		RootCAs:    roots,
		ServerName: "sync.example.test",
		MinVersion: tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("TLS dial error = %v", err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("GET " + benchmarkSyncV2HealthPath + " HTTP/1.1\r\nHost: sync.example.test\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write health request error = %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatalf("read health response error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("health response security headers = %#v", response.Header)
	}
	var health map[string]any
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health response error = %v", err)
	}
	if health["protocolVersion"] != float64(2) || health["status"] != "pairing-not-enabled" {
		t.Fatalf("health payload = %#v", health)
	}

	if _, err := newBenchmarkSyncV2DirectTLSConfig("https://other.example.test", certificatePath, privateKeyPath, "127.0.0.1:8443"); err == nil {
		t.Fatal("certificate was accepted for a different public HTTPS domain")
	}
}

func TestBenchmarkSyncV2StoreStartsAndStopsDirectTLSEndpoint(t *testing.T) {
	certificatePath, privateKeyPath, _ := writeBenchmarkSyncV2TestCertificate(t, "sync.example.test")
	store := newBenchmarkSyncV2Store(t.TempDir())
	store.identities = newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	state, err := store.ConfigureDirectTLS(
		"https://sync.example.test",
		certificatePath,
		privateKeyPath,
		reserveBenchmarkSyncV2TestListenAddress(t),
	)
	if err != nil {
		t.Fatalf("ConfigureDirectTLS() error = %v", err)
	}
	if state.EndpointStatus != "listening" || state.TLSCertificatePath != certificatePath || state.TLSPrivateKeyPath != privateKeyPath {
		t.Fatalf("configured state = %#v", state)
	}
	stopped, err := store.StopEndpoint()
	if err != nil {
		t.Fatalf("StopEndpoint() error = %v", err)
	}
	if stopped.EndpointStatus != "configured" {
		t.Fatalf("stopped endpoint status = %q, want configured", stopped.EndpointStatus)
	}
	if len(stopped.Logs) < 3 {
		t.Fatalf("direct TLS audit logs = %#v", stopped.Logs)
	}
}

func TestBenchmarkSyncV2PairingRequiresBothApprovalsAndNeverExposesSecret(t *testing.T) {
	certificatePath, privateKeyPath, certificatePEM := writeBenchmarkSyncV2TestCertificate(t, "sync.example.test")
	listenAddress := reserveBenchmarkSyncV2TestListenAddress(t)
	_, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	hostURL := "https://" + net.JoinHostPort("sync.example.test", port)

	host := newBenchmarkSyncV2Store(t.TempDir())
	host.identities = newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	if _, err := host.ConfigureDirectTLS(hostURL, certificatePath, privateKeyPath, listenAddress); err != nil {
		t.Fatalf("host ConfigureDirectTLS() error = %v", err)
	}
	defer host.Close()
	invitation, err := host.CreatePairingInvitation()
	if err != nil {
		t.Fatalf("CreatePairingInvitation() error = %v", err)
	}
	if invitation.PairingSecret == "" {
		t.Fatal("pairing invitation has no secret")
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("AppendCertsFromPEM() did not add the host certificate")
	}
	joining := newBenchmarkSyncV2Store(t.TempDir())
	joining.identities = newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	joining.httpClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "sync.example.test", MinVersion: tls.VersionTLS13},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, listenAddress)
			},
		},
	}
	request, err := joining.StartPairing(invitation.PublicHTTPSURL, invitation.PairingSecret)
	if err != nil {
		t.Fatalf("StartPairing() error = %v", err)
	}
	if request.Status != "pending" || request.Host.SigningFingerprint != invitation.Host.SigningFingerprint {
		t.Fatalf("outgoing pairing = %#v", request)
	}

	hostState, err := host.State()
	if err != nil {
		t.Fatalf("host State() error = %v", err)
	}
	if len(hostState.PendingPairings) != 1 || hostState.PendingPairings[0].RequestID != request.RequestID {
		t.Fatalf("host pending pairings = %#v", hostState.PendingPairings)
	}
	requester := hostState.PendingPairings[0].Requester
	if _, err := host.ApprovePairing(request.RequestID); err != nil {
		t.Fatalf("ApprovePairing() error = %v", err)
	}
	checked, err := joining.CheckPairing(request.RequestID)
	if err != nil {
		t.Fatalf("CheckPairing() error = %v", err)
	}
	if checked.Status != "approved" {
		t.Fatalf("CheckPairing() status = %q, want approved", checked.Status)
	}
	joinedState, err := joining.ConfirmPairing(request.RequestID)
	if err != nil {
		t.Fatalf("ConfirmPairing() error = %v", err)
	}
	if len(joinedState.Peers) != 1 || len(hostState.Peers) != 0 {
		t.Fatalf("peer state after confirmation: joining=%#v host(before refresh)=%#v", joinedState.Peers, hostState.Peers)
	}
	hostState, err = host.State()
	if err != nil {
		t.Fatalf("host State() after approval error = %v", err)
	}
	if len(hostState.Peers) != 1 || !sameBenchmarkSyncV2PeerKey(hostState.Peers[0], requester) || joinedState.Peers[0].SigningFingerprint != invitation.Host.SigningFingerprint {
		t.Fatalf("host/joining trust state is incorrect: host=%#v joining=%#v", hostState.Peers, joinedState.Peers)
	}
	stateJSON, err := json.Marshal(joinedState)
	if err != nil {
		t.Fatalf("Marshal(joined state) error = %v", err)
	}
	if strings.Contains(string(stateJSON), invitation.PairingSecret) {
		t.Fatalf("pairing secret leaked into public state: %s", stateJSON)
	}
}

func reserveBenchmarkSyncV2TestListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener error = %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved listener error = %v", err)
	}
	return address
}

func writeBenchmarkSyncV2TestCertificate(t *testing.T, domain string) (certificatePath, privateKeyPath string, certificatePEM []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatalf("serial number error = %v", err)
	}
	now := time.Now()
	certificateDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}, &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}, publicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	certificatePEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	directory := t.TempDir()
	certificatePath = filepath.Join(directory, "certificate.pem")
	privateKeyPath = filepath.Join(directory, "private-key.pem")
	if err := os.WriteFile(certificatePath, certificatePEM, 0o600); err != nil {
		t.Fatalf("write certificate error = %v", err)
	}
	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyPEM}), 0o600); err != nil {
		t.Fatalf("write private key error = %v", err)
	}
	return certificatePath, privateKeyPath, certificatePEM
}

func (s *benchmarkSyncV2MemorySecretStore) Get(service, account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.entries[service+"\x00"+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (s *benchmarkSyncV2MemorySecretStore) Set(service, account, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]string)
	}
	s.entries[service+"\x00"+account] = value
	return nil
}

func TestBenchmarkSyncV2IdentityPersistsPrivateKeysOnlyInSecretStore(t *testing.T) {
	secrets := &benchmarkSyncV2MemorySecretStore{}
	createdAt := time.Date(2026, time.September, 15, 1, 0, 0, 0, time.UTC)
	firstStore := newBenchmarkSyncV2IdentityStore(secrets)
	firstStore.now = func() time.Time { return createdAt }
	first, err := firstStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	if first.publicIdentity.SigningKeyID == "" || first.publicIdentity.KeyAgreementKeyID == "" {
		t.Fatalf("public identity lacks key IDs: %#v", first.publicIdentity)
	}
	if first.signingPrivate.Type == "" || first.agreementPrivate.Type == "" {
		t.Fatal("generated identity does not retain private keys for signing and key agreement")
	}

	secondStore := newBenchmarkSyncV2IdentityStore(secrets)
	second, err := secondStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("second LoadOrCreate() error = %v", err)
	}
	if second.publicIdentity != first.publicIdentity {
		t.Fatalf("public identity changed after reload: first=%#v second=%#v", first.publicIdentity, second.publicIdentity)
	}
	publicJSON, err := json.Marshal(first.publicIdentity)
	if err != nil {
		t.Fatalf("Marshal(public identity) error = %v", err)
	}
	if strings.Contains(string(publicJSON), "PRIVATE KEY") {
		t.Fatalf("public identity exposes private key material: %s", publicJSON)
	}
	stored, err := secrets.Get(benchmarkSyncV2KeyringService, benchmarkSyncV2KeyringAccount)
	if err != nil || !strings.Contains(stored, "PRIVATE KEY") {
		t.Fatalf("private keys were not stored in the secret store: %q, %v", stored, err)
	}
}

func TestBenchmarkSyncV2SignedEnvelopeRejectsTampering(t *testing.T) {
	now := time.Date(2026, time.September, 15, 1, 0, 0, 0, time.UTC)
	senderStore := newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	senderStore.now = func() time.Time { return now }
	sender, err := senderStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("sender LoadOrCreate() error = %v", err)
	}

	payload := []ModelBenchmark{{ID: "benchmark-1", Model: "model-a", Status: "completed"}}
	envelope, err := signBenchmarkSyncV2Envelope(sender, benchmarkSyncV2BenchmarkBatchType, payload, now)
	if err != nil {
		t.Fatalf("signBenchmarkSyncV2Envelope() error = %v", err)
	}
	verifiedPayload, err := verifyBenchmarkSyncV2Envelope(envelope, benchmarkSyncV2BenchmarkBatchType, sender.signingPublic.ToPEM(), now)
	if err != nil {
		t.Fatalf("verifyBenchmarkSyncV2Envelope() error = %v", err)
	}
	var decoded []ModelBenchmark
	if err := json.Unmarshal(verifiedPayload, &decoded); err != nil || len(decoded) != 1 || decoded[0].ID != payload[0].ID {
		t.Fatalf("verified payload = %#v, %v", decoded, err)
	}

	tamperedPayload := envelope
	tamperedPayload.Payload = json.RawMessage(`[{"id":"benchmark-1","model":"modified"}]`)
	if _, err := verifyBenchmarkSyncV2Envelope(tamperedPayload, benchmarkSyncV2BenchmarkBatchType, sender.signingPublic.ToPEM(), now); err == nil {
		t.Fatal("payload modification was accepted")
	}

	tamperedSignature := envelope
	if strings.HasPrefix(tamperedSignature.Signature, "A") {
		tamperedSignature.Signature = "B" + tamperedSignature.Signature[1:]
	} else {
		tamperedSignature.Signature = "A" + tamperedSignature.Signature[1:]
	}
	if _, err := verifyBenchmarkSyncV2Envelope(tamperedSignature, benchmarkSyncV2BenchmarkBatchType, sender.signingPublic.ToPEM(), now); err == nil {
		t.Fatal("signature modification was accepted")
	}

	tamperedSender := envelope
	tamperedSender.SenderKeyID = "anp:signing:other-device"
	if _, err := verifyBenchmarkSyncV2Envelope(tamperedSender, benchmarkSyncV2BenchmarkBatchType, sender.signingPublic.ToPEM(), now); err == nil {
		t.Fatal("sender key ID modification was accepted")
	}

	receiverStore := newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	receiverStore.now = func() time.Time { return now }
	receiver, err := receiverStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("receiver LoadOrCreate() error = %v", err)
	}
	if _, err := verifyBenchmarkSyncV2Envelope(envelope, benchmarkSyncV2BenchmarkBatchType, receiver.signingPublic.ToPEM(), now); err == nil {
		t.Fatal("a different device key was accepted")
	}
}

func TestBenchmarkSyncV2SignedEnvelopeRejectsExpiredMessages(t *testing.T) {
	now := time.Date(2026, time.September, 15, 1, 0, 0, 0, time.UTC)
	store := newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	store.now = func() time.Time { return now }
	keySet, err := store.LoadOrCreate()
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	envelope, err := signBenchmarkSyncV2Envelope(keySet, benchmarkSyncV2BenchmarkBatchType, []ModelBenchmark{{ID: "benchmark-1"}}, now)
	if err != nil {
		t.Fatalf("signBenchmarkSyncV2Envelope() error = %v", err)
	}
	if _, err := verifyBenchmarkSyncV2Envelope(envelope, benchmarkSyncV2BenchmarkBatchType, keySet.signingPublic.ToPEM(), now.Add(benchmarkSyncV2EnvelopeMaxAge+time.Nanosecond)); err == nil {
		t.Fatal("expired signed message was accepted")
	}
}

func TestBenchmarkSyncV2RecordProofSurvivesImportMetadataAndRejectsChanges(t *testing.T) {
	now := time.Date(2026, time.September, 15, 1, 0, 0, 0, time.UTC)
	identityStore := newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	identityStore.now = func() time.Time { return now }
	keySet, err := identityStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}

	signed, err := signBenchmarkSyncV2Record(keySet, completedBenchmarkForReport("origin-benchmark"), now)
	if err != nil {
		t.Fatalf("signBenchmarkSyncV2Record() error = %v", err)
	}
	if signed.Proof == nil || signed.Proof.OriginBenchmarkID != "origin-benchmark" {
		t.Fatalf("signed record proof = %#v", signed.Proof)
	}
	if err := verifyBenchmarkSyncV2RecordProof(signed, keySet.signingPublic.ToPEM()); err != nil {
		t.Fatalf("verify local proof error = %v", err)
	}

	imported := signed
	imported.ID = "local-copy"
	imported.Imported = true
	imported.Source = benchmarkSourceSync
	imported.OriginDeviceID = keySet.publicIdentity.SigningKeyID
	imported.OriginDeviceName = "원본 PC"
	imported.OriginBenchmarkID = signed.Proof.OriginBenchmarkID
	if err := verifyBenchmarkSyncV2RecordProof(imported, keySet.signingPublic.ToPEM()); err != nil {
		t.Fatalf("proof did not survive import metadata: %v", err)
	}

	tampered := imported
	tampered.Cases = append([]ModelBenchmarkCase(nil), imported.Cases...)
	tampered.Cases[0].Content = "바뀐 응답"
	if err := verifyBenchmarkSyncV2RecordProof(tampered, keySet.signingPublic.ToPEM()); err == nil {
		t.Fatal("modified benchmark content was accepted")
	}

	differentStore := newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	differentStore.now = func() time.Time { return now }
	differentKeySet, err := differentStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("different LoadOrCreate() error = %v", err)
	}
	if err := verifyBenchmarkSyncV2RecordProof(imported, differentKeySet.signingPublic.ToPEM()); err == nil {
		t.Fatal("proof was accepted with a different trusted peer key")
	}
}

func TestBenchmarkSyncV2RecordProofPersistsAndIsClearedByLocalEdit(t *testing.T) {
	now := time.Date(2026, time.September, 15, 1, 0, 0, 0, time.UTC)
	identityStore := newBenchmarkSyncV2IdentityStore(&benchmarkSyncV2MemorySecretStore{})
	identityStore.now = func() time.Time { return now }
	keySet, err := identityStore.LoadOrCreate()
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	store := newModelBenchmarkStore(t.TempDir())
	created, err := store.Create(completedBenchmarkForReport("proof-storage"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	signed, err := signBenchmarkSyncV2Record(keySet, created, now)
	if err != nil {
		t.Fatalf("signBenchmarkSyncV2Record() error = %v", err)
	}
	if _, err := store.SaveV2Proof(signed); err != nil {
		t.Fatalf("SaveV2Proof() error = %v", err)
	}
	opened, err := store.Open(created.ID)
	if err != nil || opened.Proof == nil {
		t.Fatalf("Open() proof = %#v, %v", opened.Proof, err)
	}
	if err := verifyBenchmarkSyncV2RecordProof(opened, keySet.signingPublic.ToPEM()); err != nil {
		t.Fatalf("stored proof did not verify: %v", err)
	}

	opened.Cases[0].Content = "사용자가 고친 응답"
	edited, err := store.Save(opened)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if edited.Proof != nil {
		t.Fatalf("local edit retained stale proof: %#v", edited.Proof)
	}
}
