package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	anp "github.com/agent-network-protocol/anp/golang"
)

const (
	benchmarkSyncV2PairPath              = "/agent-chat/benchmark-sync/v2/pair"
	benchmarkSyncV2PairStatusPath        = "/agent-chat/benchmark-sync/v2/pair/status"
	benchmarkSyncV2PairRequestType       = "pairing-request"
	benchmarkSyncV2PairStatusRequestType = "pairing-status-request"
	benchmarkSyncV2PairResponseType      = "pairing-response"
	benchmarkSyncV2PairingVersion        = 1
	benchmarkSyncV2PairingLifetime       = 15 * time.Minute
	benchmarkSyncV2PairingMaxBodyBytes   = 64 << 10
)

type benchmarkSyncV2InvitationState struct {
	secret    string
	expiresAt time.Time
	requestID string
}

type benchmarkSyncV2PendingPairingState struct {
	requestID string
	requester BenchmarkSyncV2Peer
	expiresAt time.Time
	status    string
}

type benchmarkSyncV2OutgoingPairingState struct {
	requestID string
	secret    string
	host      BenchmarkSyncV2Peer
	expiresAt time.Time
	status    string
}

type benchmarkSyncV2PairingRequestPayload struct {
	Version       int                 `json:"version"`
	RequestID     string              `json:"requestID"`
	PairingSecret string              `json:"pairingSecret"`
	Requester     BenchmarkSyncV2Peer `json:"requester"`
}

type benchmarkSyncV2PairingStatusRequestPayload struct {
	Version       int    `json:"version"`
	RequestID     string `json:"requestID"`
	PairingSecret string `json:"pairingSecret"`
	SigningKeyID  string `json:"signingKeyID"`
}

type benchmarkSyncV2PairingResponsePayload struct {
	Version   int                 `json:"version"`
	RequestID string              `json:"requestID"`
	Status    string              `json:"status"`
	Host      BenchmarkSyncV2Peer `json:"host"`
	ExpiresAt string              `json:"expiresAt"`
}

type benchmarkSyncV2PairingWireResponse struct {
	SigningPublicKeyPEM string                        `json:"signingPublicKeyPEM"`
	Envelope            benchmarkSyncV2SignedEnvelope `json:"envelope"`
}

func newBenchmarkSyncV2HTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// CreatePairingInvitation starts an in-memory, one-use v2 invitation. The
// secret is returned to the caller once and intentionally excluded from State.
func (s *benchmarkSyncV2Store) CreatePairingInvitation() (BenchmarkSyncV2Invitation, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2Invitation{}, err
	}
	if err := s.StartConfiguredEndpoint(); err != nil {
		return BenchmarkSyncV2Invitation{}, err
	}
	secret, err := newBenchmarkSyncV2PairingSecret()
	if err != nil {
		return BenchmarkSyncV2Invitation{}, err
	}
	expiresAt := time.Now().Add(benchmarkSyncV2PairingLifetime).UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2Invitation{}, err
	}
	if s.endpoint == nil || !s.hasDirectTLSConfigurationLocked() {
		return BenchmarkSyncV2Invitation{}, errors.New("v2 HTTPS 수신 endpoint가 실행 중이 아닙니다")
	}
	s.cleanupPairingsLocked(time.Now())
	s.invitations[benchmarkSyncV2PairingSecretID(secret)] = benchmarkSyncV2InvitationState{
		secret:    secret,
		expiresAt: expiresAt,
	}
	s.appendLogLocked("pairing-invitation-created", "completed", "v2 페어링 초대를 만들었습니다")
	if err := s.saveLocked(); err != nil {
		return BenchmarkSyncV2Invitation{}, err
	}
	return BenchmarkSyncV2Invitation{
		PublicHTTPSURL: s.state.PublicHTTPSURL,
		PairingSecret:  secret,
		ExpiresAt:      expiresAt.Format(time.RFC3339Nano),
		Host:           keySet.publicIdentity,
	}, nil
}

// StartPairing sends a signed request through normal TLS verification. It
// does not create a trusted peer yet: the remote fingerprint must be compared
// by the local user and confirmed after the host approves the request.
func (s *benchmarkSyncV2Store) StartPairing(publicHTTPSURL, pairingSecret string) (BenchmarkSyncV2OutgoingPairing, error) {
	publicHTTPSURL, err := normalizeBenchmarkSyncV2PublicHTTPSURL(publicHTTPSURL)
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	if err := validateBenchmarkSyncV2PairingSecret(pairingSecret); err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	requestID, err := newBenchmarkSyncV2Nonce()
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	requester, err := s.localPeer(keySet, "")
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	payload := benchmarkSyncV2PairingRequestPayload{
		Version:       benchmarkSyncV2PairingVersion,
		RequestID:     requestID,
		PairingSecret: pairingSecret,
		Requester:     requester,
	}
	envelope, err := signBenchmarkSyncV2Envelope(keySet, benchmarkSyncV2PairRequestType, payload, time.Now())
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	response, err := s.postPairingEnvelope(publicHTTPSURL, benchmarkSyncV2PairPath, envelope)
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	if response.RequestID != requestID || response.Status != "pending" {
		return BenchmarkSyncV2OutgoingPairing{}, errors.New("v2 페어링 요청 응답이 올바르지 않습니다")
	}
	if err := validateBenchmarkSyncV2Peer(response.Host, false); err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.ExpiresAt)
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, errors.New("v2 페어링 요청의 만료 시각이 올바르지 않습니다")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	s.cleanupPairingsLocked(time.Now())
	s.outgoingPairings[requestID] = benchmarkSyncV2OutgoingPairingState{
		requestID: requestID,
		secret:    pairingSecret,
		host:      response.Host,
		expiresAt: expiresAt,
		status:    "pending",
	}
	s.appendLogLocked("pairing-request-sent", "completed", "v2 페어링 요청을 보냈습니다")
	if err := s.saveLocked(); err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	return s.publicOutgoingPairingLocked(s.outgoingPairings[requestID]), nil
}

// CheckPairing reuses the original secret and signed device key. It never
// trusts an accepted peer automatically; ConfirmPairing performs that user
// acknowledgement separately.
func (s *benchmarkSyncV2Store) CheckPairing(requestID string) (BenchmarkSyncV2OutgoingPairing, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	s.mu.Lock()
	if err := s.ensureLoadedLocked(); err != nil {
		s.mu.Unlock()
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	pending, ok := s.outgoingPairings[strings.TrimSpace(requestID)]
	s.mu.Unlock()
	if !ok || time.Now().After(pending.expiresAt) {
		return BenchmarkSyncV2OutgoingPairing{}, errors.New("확인할 v2 페어링 요청이 없거나 만료되었습니다")
	}
	payload := benchmarkSyncV2PairingStatusRequestPayload{
		Version:       benchmarkSyncV2PairingVersion,
		RequestID:     pending.requestID,
		PairingSecret: pending.secret,
		SigningKeyID:  keySet.publicIdentity.SigningKeyID,
	}
	envelope, err := signBenchmarkSyncV2Envelope(keySet, benchmarkSyncV2PairStatusRequestType, payload, time.Now())
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	response, err := s.postPairingEnvelope(pending.host.PublicHTTPSURL, benchmarkSyncV2PairStatusPath, envelope)
	if err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	if response.RequestID != pending.requestID || response.Status == "" || !sameBenchmarkSyncV2PeerKey(pending.host, response.Host) {
		return BenchmarkSyncV2OutgoingPairing{}, errors.New("v2 페어링 상태 응답의 장치 키가 일치하지 않습니다")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.outgoingPairings[pending.requestID]
	if !ok {
		return BenchmarkSyncV2OutgoingPairing{}, errors.New("v2 페어링 요청이 더 이상 없습니다")
	}
	current.status = response.Status
	s.outgoingPairings[pending.requestID] = current
	s.appendLogLocked("pairing-status-checked", "completed", "v2 페어링 요청 상태를 확인했습니다")
	if err := s.saveLocked(); err != nil {
		return BenchmarkSyncV2OutgoingPairing{}, err
	}
	return s.publicOutgoingPairingLocked(current), nil
}

// ConfirmPairing stores the host key only after the local user has compared
// its fingerprint and after the host has approved the request.
func (s *benchmarkSyncV2Store) ConfirmPairing(requestID string) (BenchmarkSyncV2State, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	pending, ok := s.outgoingPairings[strings.TrimSpace(requestID)]
	if !ok || pending.status != "approved" {
		return BenchmarkSyncV2State{}, errors.New("상대 장치가 승인한 v2 페어링 요청만 확인할 수 있습니다")
	}
	pending.host.TrustedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.upsertPeerLocked(pending.host)
	delete(s.outgoingPairings, pending.requestID)
	s.appendLogLocked("pairing-confirmed", "completed", "상대 장치 fingerprint 확인을 마쳤습니다")
	if err := s.saveLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.publicStateLocked(keySet.publicIdentity), nil
}

func (s *benchmarkSyncV2Store) ApprovePairing(requestID string) (BenchmarkSyncV2State, error) {
	return s.resolvePairing(requestID, "approved")
}

func (s *benchmarkSyncV2Store) RejectPairing(requestID string) (BenchmarkSyncV2State, error) {
	return s.resolvePairing(requestID, "rejected")
}

func (s *benchmarkSyncV2Store) resolvePairing(requestID, status string) (BenchmarkSyncV2State, error) {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return BenchmarkSyncV2State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	pending, ok := s.pendingPairings[strings.TrimSpace(requestID)]
	if !ok || time.Now().After(pending.expiresAt) || pending.status != "pending" {
		return BenchmarkSyncV2State{}, errors.New("처리할 v2 페어링 요청이 없거나 만료되었습니다")
	}
	pending.status = status
	s.pendingPairings[pending.requestID] = pending
	if status == "approved" {
		pending.requester.TrustedAt = time.Now().UTC().Format(time.RFC3339Nano)
		s.upsertPeerLocked(pending.requester)
		s.appendLogLocked("pairing-approved", "completed", "v2 페어링 요청을 승인했습니다")
	} else {
		s.appendLogLocked("pairing-rejected", "completed", "v2 페어링 요청을 거절했습니다")
	}
	if err := s.saveLocked(); err != nil {
		return BenchmarkSyncV2State{}, err
	}
	return s.publicStateLocked(keySet.publicIdentity), nil
}

func (s *benchmarkSyncV2Store) handlePairingRequest(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	envelope, err := decodeBenchmarkSyncV2Envelope(request)
	if err != nil {
		http.Error(writer, "invalid pairing request", http.StatusBadRequest)
		return
	}
	var unverified benchmarkSyncV2PairingRequestPayload
	if err := json.Unmarshal(envelope.Payload, &unverified); err != nil || validateBenchmarkSyncV2Peer(unverified.Requester, false) != nil {
		http.Error(writer, "invalid pairing request", http.StatusBadRequest)
		return
	}
	payloadBytes, err := verifyBenchmarkSyncV2Envelope(envelope, benchmarkSyncV2PairRequestType, unverified.Requester.SigningPublicKeyPEM, time.Now())
	if err != nil {
		http.Error(writer, "invalid pairing request", http.StatusUnauthorized)
		return
	}
	var payload benchmarkSyncV2PairingRequestPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil || payload.Version != benchmarkSyncV2PairingVersion || payload.RequestID == "" || validateBenchmarkSyncV2PairingSecret(payload.PairingSecret) != nil || validateBenchmarkSyncV2Peer(payload.Requester, false) != nil {
		http.Error(writer, "invalid pairing request", http.StatusBadRequest)
		return
	}
	if err := s.receivePairingRequest(payload); err != nil {
		http.Error(writer, "pairing request unavailable", http.StatusGone)
		return
	}
	if err := s.writePairingResponse(writer, payload.RequestID); err != nil {
		http.Error(writer, "pairing response unavailable", http.StatusInternalServerError)
	}
}

func (s *benchmarkSyncV2Store) handlePairingStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	envelope, err := decodeBenchmarkSyncV2Envelope(request)
	if err != nil {
		http.Error(writer, "invalid pairing status request", http.StatusBadRequest)
		return
	}
	var unverified benchmarkSyncV2PairingStatusRequestPayload
	if err := json.Unmarshal(envelope.Payload, &unverified); err != nil || unverified.SigningKeyID == "" {
		http.Error(writer, "invalid pairing status request", http.StatusBadRequest)
		return
	}
	peer, err := s.pendingPairingRequester(unverified.RequestID)
	if err != nil {
		http.Error(writer, "pairing request unavailable", http.StatusGone)
		return
	}
	payloadBytes, err := verifyBenchmarkSyncV2Envelope(envelope, benchmarkSyncV2PairStatusRequestType, peer.SigningPublicKeyPEM, time.Now())
	if err != nil {
		http.Error(writer, "invalid pairing status request", http.StatusUnauthorized)
		return
	}
	var payload benchmarkSyncV2PairingStatusRequestPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil || payload.Version != benchmarkSyncV2PairingVersion || payload.RequestID != unverified.RequestID || payload.SigningKeyID != peer.SigningKeyID || validateBenchmarkSyncV2PairingSecret(payload.PairingSecret) != nil {
		http.Error(writer, "invalid pairing status request", http.StatusBadRequest)
		return
	}
	if err := s.verifyPairingStatusSecret(payload); err != nil {
		http.Error(writer, "pairing request unavailable", http.StatusGone)
		return
	}
	if err := s.writePairingResponse(writer, payload.RequestID); err != nil {
		http.Error(writer, "pairing response unavailable", http.StatusInternalServerError)
	}
}

func (s *benchmarkSyncV2Store) receivePairingRequest(payload benchmarkSyncV2PairingRequestPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}
	now := time.Now()
	s.cleanupPairingsLocked(now)
	invitationKey := benchmarkSyncV2PairingSecretID(payload.PairingSecret)
	invitation, ok := s.invitations[invitationKey]
	if !ok || now.After(invitation.expiresAt) || subtle.ConstantTimeCompare([]byte(invitation.secret), []byte(payload.PairingSecret)) != 1 {
		return errors.New("invalid invitation")
	}
	if invitation.requestID != "" && invitation.requestID != payload.RequestID {
		return errors.New("invitation already used")
	}
	if existing, ok := s.pendingPairings[payload.RequestID]; ok {
		if !sameBenchmarkSyncV2PeerKey(existing.requester, payload.Requester) {
			return errors.New("request key changed")
		}
		return nil
	}
	invitation.requestID = payload.RequestID
	s.invitations[invitationKey] = invitation
	s.pendingPairings[payload.RequestID] = benchmarkSyncV2PendingPairingState{
		requestID: payload.RequestID,
		requester: payload.Requester,
		expiresAt: invitation.expiresAt,
		status:    "pending",
	}
	s.appendLogLocked("pairing-request-received", "completed", "v2 페어링 요청을 받았습니다")
	return s.saveLocked()
}

func (s *benchmarkSyncV2Store) pendingPairingRequester(requestID string) (BenchmarkSyncV2Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2Peer{}, err
	}
	pending, ok := s.pendingPairings[strings.TrimSpace(requestID)]
	if !ok || time.Now().After(pending.expiresAt) {
		return BenchmarkSyncV2Peer{}, errors.New("pairing not found")
	}
	return pending.requester, nil
}

func (s *benchmarkSyncV2Store) verifyPairingStatusSecret(payload benchmarkSyncV2PairingStatusRequestPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pendingPairings[payload.RequestID]
	if !ok || time.Now().After(pending.expiresAt) || pending.requester.SigningKeyID != payload.SigningKeyID {
		return errors.New("pairing not found")
	}
	for _, invitation := range s.invitations {
		if invitation.requestID == payload.RequestID && subtle.ConstantTimeCompare([]byte(invitation.secret), []byte(payload.PairingSecret)) == 1 {
			return nil
		}
	}
	return errors.New("pairing secret mismatch")
}

func (s *benchmarkSyncV2Store) writePairingResponse(writer http.ResponseWriter, requestID string) error {
	keySet, err := s.identities.LoadOrCreate()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if err := s.ensureLoadedLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	pending, ok := s.pendingPairings[requestID]
	if !ok {
		s.mu.Unlock()
		return errors.New("pairing not found")
	}
	publicHTTPSURL := s.state.PublicHTTPSURL
	s.mu.Unlock()
	host, err := s.localPeer(keySet, publicHTTPSURL)
	if err != nil {
		return err
	}
	payload := benchmarkSyncV2PairingResponsePayload{
		Version:   benchmarkSyncV2PairingVersion,
		RequestID: requestID,
		Status:    pending.status,
		Host:      host,
		ExpiresAt: pending.expiresAt.UTC().Format(time.RFC3339Nano),
	}
	envelope, err := signBenchmarkSyncV2Envelope(keySet, benchmarkSyncV2PairResponseType, payload, time.Now())
	if err != nil {
		return err
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(writer).Encode(benchmarkSyncV2PairingWireResponse{
		SigningPublicKeyPEM: keySet.signingPublic.ToPEM(),
		Envelope:            envelope,
	})
}

func (s *benchmarkSyncV2Store) postPairingEnvelope(publicHTTPSURL, endpointPath string, envelope benchmarkSyncV2SignedEnvelope) (benchmarkSyncV2PairingResponsePayload, error) {
	endpointURL, err := benchmarkSyncV2EndpointURL(publicHTTPSURL, endpointPath)
	if err != nil {
		return benchmarkSyncV2PairingResponsePayload{}, err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return benchmarkSyncV2PairingResponsePayload{}, err
	}
	request, err := http.NewRequest(http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return benchmarkSyncV2PairingResponsePayload{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	s.mu.Lock()
	client := s.httpClient
	s.mu.Unlock()
	if client == nil {
		client = newBenchmarkSyncV2HTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return benchmarkSyncV2PairingResponsePayload{}, errors.New("v2 HTTPS 페어링 요청을 보낼 수 없습니다")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return benchmarkSyncV2PairingResponsePayload{}, errors.New("v2 페어링 요청이 거절되었거나 만료되었습니다")
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, benchmarkSyncV2PairingMaxBodyBytes))
	if err != nil {
		return benchmarkSyncV2PairingResponsePayload{}, errors.New("v2 페어링 응답을 읽을 수 없습니다")
	}
	var wire benchmarkSyncV2PairingWireResponse
	if err := json.Unmarshal(contents, &wire); err != nil {
		return benchmarkSyncV2PairingResponsePayload{}, errors.New("v2 페어링 응답 형식이 올바르지 않습니다")
	}
	var unverified benchmarkSyncV2PairingResponsePayload
	if err := json.Unmarshal(wire.Envelope.Payload, &unverified); err != nil || validateBenchmarkSyncV2Peer(unverified.Host, true) != nil || wire.SigningPublicKeyPEM != unverified.Host.SigningPublicKeyPEM {
		return benchmarkSyncV2PairingResponsePayload{}, errors.New("v2 페어링 응답의 장치 신원이 올바르지 않습니다")
	}
	payloadBytes, err := verifyBenchmarkSyncV2Envelope(wire.Envelope, benchmarkSyncV2PairResponseType, wire.SigningPublicKeyPEM, time.Now())
	if err != nil {
		return benchmarkSyncV2PairingResponsePayload{}, errors.New("v2 페어링 응답 서명을 검증할 수 없습니다")
	}
	var payload benchmarkSyncV2PairingResponsePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil || payload.Version != benchmarkSyncV2PairingVersion || !sameBenchmarkSyncV2PeerKey(unverified.Host, payload.Host) {
		return benchmarkSyncV2PairingResponsePayload{}, errors.New("v2 페어링 응답 본문이 올바르지 않습니다")
	}
	return payload, nil
}

func decodeBenchmarkSyncV2Envelope(request *http.Request) (benchmarkSyncV2SignedEnvelope, error) {
	contents, err := io.ReadAll(io.LimitReader(request.Body, benchmarkSyncV2PairingMaxBodyBytes+1))
	if err != nil || len(contents) == 0 || len(contents) > benchmarkSyncV2PairingMaxBodyBytes {
		return benchmarkSyncV2SignedEnvelope{}, errors.New("empty request")
	}
	var envelope benchmarkSyncV2SignedEnvelope
	if err := json.Unmarshal(contents, &envelope); err != nil {
		return benchmarkSyncV2SignedEnvelope{}, err
	}
	return envelope, nil
}

func benchmarkSyncV2EndpointURL(publicHTTPSURL, endpointPath string) (string, error) {
	base, err := url.Parse(publicHTTPSURL)
	if err != nil {
		return "", errors.New("v2 공개 HTTPS 주소가 올바르지 않습니다")
	}
	base.Path = strings.TrimRight(base.Path, "/") + endpointPath
	base.RawPath = ""
	return base.String(), nil
}

func (s *benchmarkSyncV2Store) localPeer(keySet benchmarkSyncV2KeySet, publicHTTPSURL string) (BenchmarkSyncV2Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return BenchmarkSyncV2Peer{}, err
	}
	if publicHTTPSURL != "" {
		normalizedURL, err := normalizeBenchmarkSyncV2PublicHTTPSURL(publicHTTPSURL)
		if err != nil {
			return BenchmarkSyncV2Peer{}, err
		}
		publicHTTPSURL = normalizedURL
	}
	return BenchmarkSyncV2Peer{
		DeviceName:               s.state.DeviceName,
		PublicHTTPSURL:           publicHTTPSURL,
		SigningKeyID:             keySet.publicIdentity.SigningKeyID,
		SigningPublicKeyPEM:      keySet.signingPublic.ToPEM(),
		SigningFingerprint:       keySet.publicIdentity.SigningFingerprint,
		KeyAgreementKeyID:        keySet.publicIdentity.KeyAgreementKeyID,
		KeyAgreementPublicKeyPEM: keySet.agreementPublic.ToPEM(),
		KeyAgreementFingerprint:  keySet.publicIdentity.KeyAgreementFingerprint,
	}, nil
}

func validateBenchmarkSyncV2Peer(peer BenchmarkSyncV2Peer, requirePublicHTTPSURL bool) error {
	if _, err := normalizeBenchmarkSyncV2DeviceName(peer.DeviceName); err != nil {
		return errors.New("v2 장치 이름이 올바르지 않습니다")
	}
	if requirePublicHTTPSURL {
		if _, err := normalizeBenchmarkSyncV2PublicHTTPSURL(peer.PublicHTTPSURL); err != nil {
			return errors.New("v2 장치의 외부 HTTPS 주소가 올바르지 않습니다")
		}
	} else if peer.PublicHTTPSURL != "" {
		if _, err := normalizeBenchmarkSyncV2PublicHTTPSURL(peer.PublicHTTPSURL); err != nil {
			return errors.New("v2 장치의 외부 HTTPS 주소가 올바르지 않습니다")
		}
	}
	signingPublic, err := anp.PublicKeyFromPEM(peer.SigningPublicKeyPEM)
	if err != nil || signingPublic.Type != anp.KeyTypeEd25519 || peer.SigningKeyID == "" || peer.SigningFingerprint == "" {
		return errors.New("v2 장치의 서명 키가 올바르지 않습니다")
	}
	agreementPublic, err := anp.PublicKeyFromPEM(peer.KeyAgreementPublicKeyPEM)
	if err != nil || agreementPublic.Type != anp.KeyTypeX25519 || peer.KeyAgreementKeyID == "" || peer.KeyAgreementFingerprint == "" {
		return errors.New("v2 장치의 암호화 키가 올바르지 않습니다")
	}
	if subtle.ConstantTimeCompare([]byte(peer.SigningKeyID), []byte(benchmarkSyncV2KeyID("signing", signingPublic))) != 1 || subtle.ConstantTimeCompare([]byte(peer.SigningFingerprint), []byte(benchmarkSyncV2Fingerprint(signingPublic))) != 1 || subtle.ConstantTimeCompare([]byte(peer.KeyAgreementKeyID), []byte(benchmarkSyncV2KeyID("agreement", agreementPublic))) != 1 || subtle.ConstantTimeCompare([]byte(peer.KeyAgreementFingerprint), []byte(benchmarkSyncV2Fingerprint(agreementPublic))) != 1 {
		return errors.New("v2 장치 키 식별자가 일치하지 않습니다")
	}
	return nil
}

func sameBenchmarkSyncV2PeerKey(left, right BenchmarkSyncV2Peer) bool {
	return subtle.ConstantTimeCompare([]byte(left.SigningKeyID), []byte(right.SigningKeyID)) == 1 && subtle.ConstantTimeCompare([]byte(left.SigningPublicKeyPEM), []byte(right.SigningPublicKeyPEM)) == 1 && subtle.ConstantTimeCompare([]byte(left.KeyAgreementKeyID), []byte(right.KeyAgreementKeyID)) == 1 && subtle.ConstantTimeCompare([]byte(left.KeyAgreementPublicKeyPEM), []byte(right.KeyAgreementPublicKeyPEM)) == 1
}

func (s *benchmarkSyncV2Store) upsertPeerLocked(peer BenchmarkSyncV2Peer) {
	for index, existing := range s.state.Peers {
		if existing.SigningKeyID == peer.SigningKeyID {
			s.state.Peers[index] = peer
			return
		}
	}
	s.state.Peers = append(s.state.Peers, peer)
}

func (s *benchmarkSyncV2Store) publicPendingPairingsLocked() []BenchmarkSyncV2PendingPairing {
	now := time.Now()
	s.cleanupPairingsLocked(now)
	values := make([]BenchmarkSyncV2PendingPairing, 0, len(s.pendingPairings))
	for _, pending := range s.pendingPairings {
		if pending.status == "pending" {
			values = append(values, BenchmarkSyncV2PendingPairing{RequestID: pending.requestID, Requester: pending.requester, ExpiresAt: pending.expiresAt.UTC().Format(time.RFC3339Nano)})
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ExpiresAt < values[right].ExpiresAt })
	return values
}

func (s *benchmarkSyncV2Store) publicOutgoingPairingsLocked() []BenchmarkSyncV2OutgoingPairing {
	now := time.Now()
	s.cleanupPairingsLocked(now)
	values := make([]BenchmarkSyncV2OutgoingPairing, 0, len(s.outgoingPairings))
	for _, pending := range s.outgoingPairings {
		values = append(values, s.publicOutgoingPairingLocked(pending))
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ExpiresAt < values[right].ExpiresAt })
	return values
}

func (s *benchmarkSyncV2Store) publicOutgoingPairingLocked(value benchmarkSyncV2OutgoingPairingState) BenchmarkSyncV2OutgoingPairing {
	return BenchmarkSyncV2OutgoingPairing{RequestID: value.requestID, Host: value.host, Status: value.status, ExpiresAt: value.expiresAt.UTC().Format(time.RFC3339Nano)}
}

func (s *benchmarkSyncV2Store) cleanupPairingsLocked(now time.Time) {
	for identifier, invitation := range s.invitations {
		if now.After(invitation.expiresAt) {
			delete(s.invitations, identifier)
		}
	}
	for identifier, pairing := range s.pendingPairings {
		if now.After(pairing.expiresAt) {
			delete(s.pendingPairings, identifier)
		}
	}
	for identifier, pairing := range s.outgoingPairings {
		if now.After(pairing.expiresAt) {
			delete(s.outgoingPairings, identifier)
		}
	}
}

func newBenchmarkSyncV2PairingSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("v2 페어링 secret을 만들 수 없습니다: %w", err)
	}
	return anp.EncodeBase64URL(value), nil
}

func validateBenchmarkSyncV2PairingSecret(value string) error {
	decoded, err := anp.DecodeBase64URL(strings.TrimSpace(value))
	if err != nil || len(decoded) != 32 {
		return errors.New("v2 페어링 secret이 올바르지 않습니다")
	}
	return nil
}

func benchmarkSyncV2PairingSecretID(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return anp.EncodeBase64URL(digest[:])
}
