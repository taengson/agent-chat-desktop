package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	anp "github.com/agent-network-protocol/anp/golang"
)

const (
	benchmarkSyncV2EnvelopeVersion    = 2
	benchmarkSyncV2EnvelopeMaxAge     = 10 * time.Minute
	benchmarkSyncV2EnvelopeClockSkew  = 2 * time.Minute
	benchmarkSyncV2BenchmarkBatchType = "benchmark-batch"
)

// benchmarkSyncV2SignedEnvelope is the signed inner payload used by v2. A
// later E2EE layer encrypts this entire object; keeping the signature inside
// encryption lets a receiver prove who produced a benchmark after decryption.
type benchmarkSyncV2SignedEnvelope struct {
	Version       int             `json:"version"`
	Type          string          `json:"type"`
	SenderKeyID   string          `json:"senderKeyID"`
	CreatedAt     string          `json:"createdAt"`
	Nonce         string          `json:"nonce"`
	PayloadSHA256 string          `json:"payloadSHA256"`
	Payload       json.RawMessage `json:"payload"`
	Signature     string          `json:"signature"`
}

type benchmarkSyncV2SignatureInput struct {
	Version       int    `json:"version"`
	Type          string `json:"type"`
	SenderKeyID   string `json:"senderKeyID"`
	CreatedAt     string `json:"createdAt"`
	Nonce         string `json:"nonce"`
	PayloadSHA256 string `json:"payloadSHA256"`
}

func signBenchmarkSyncV2Envelope(keySet benchmarkSyncV2KeySet, envelopeType string, payload any, now time.Time) (benchmarkSyncV2SignedEnvelope, error) {
	envelopeType = strings.TrimSpace(envelopeType)
	if envelopeType == "" {
		return benchmarkSyncV2SignedEnvelope{}, errors.New("v2 메시지 종류가 비어 있습니다")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return benchmarkSyncV2SignedEnvelope{}, fmt.Errorf("v2 메시지를 인코딩할 수 없습니다: %w", err)
	}
	nonce, err := newBenchmarkSyncV2Nonce()
	if err != nil {
		return benchmarkSyncV2SignedEnvelope{}, err
	}
	digest := sha256.Sum256(payloadBytes)
	envelope := benchmarkSyncV2SignedEnvelope{
		Version:       benchmarkSyncV2EnvelopeVersion,
		Type:          envelopeType,
		SenderKeyID:   keySet.publicIdentity.SigningKeyID,
		CreatedAt:     now.UTC().Format(time.RFC3339Nano),
		Nonce:         nonce,
		PayloadSHA256: anp.EncodeBase64URL(digest[:]),
		Payload:       append(json.RawMessage(nil), payloadBytes...),
	}
	signatureBase, err := benchmarkSyncV2EnvelopeSignatureBase(envelope)
	if err != nil {
		return benchmarkSyncV2SignedEnvelope{}, err
	}
	signature, err := keySet.signingPrivate.SignMessage(signatureBase)
	if err != nil {
		return benchmarkSyncV2SignedEnvelope{}, fmt.Errorf("v2 메시지에 서명할 수 없습니다: %w", err)
	}
	envelope.Signature = anp.EncodeBase64URL(signature)
	return envelope, nil
}

func verifyBenchmarkSyncV2Envelope(envelope benchmarkSyncV2SignedEnvelope, expectedType, senderPublicKeyPEM string, now time.Time) ([]byte, error) {
	if envelope.Version != benchmarkSyncV2EnvelopeVersion {
		return nil, errors.New("지원하지 않는 v2 메시지 버전입니다")
	}
	if strings.TrimSpace(envelope.Type) == "" || strings.TrimSpace(envelope.Nonce) == "" || strings.TrimSpace(envelope.Signature) == "" {
		return nil, errors.New("v2 서명 메시지의 필수 항목이 없습니다")
	}
	if expectedType != "" && envelope.Type != expectedType {
		return nil, errors.New("예상하지 않은 v2 메시지 종류입니다")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, envelope.CreatedAt)
	if err != nil || createdAt.After(now.Add(benchmarkSyncV2EnvelopeClockSkew)) || now.Sub(createdAt) > benchmarkSyncV2EnvelopeMaxAge {
		return nil, errors.New("v2 서명 메시지의 유효 시간이 지났습니다")
	}
	if len(envelope.Payload) == 0 {
		return nil, errors.New("v2 서명 메시지에 본문이 없습니다")
	}
	decodedDigest, err := anp.DecodeBase64URL(envelope.PayloadSHA256)
	if err != nil || len(decodedDigest) != sha256.Size {
		return nil, errors.New("v2 메시지 본문 digest가 올바르지 않습니다")
	}
	computedDigest := sha256.Sum256(envelope.Payload)
	if subtle.ConstantTimeCompare(decodedDigest, computedDigest[:]) != 1 {
		return nil, errors.New("v2 메시지 본문이 전송 중 변경되었습니다")
	}
	publicKey, err := anp.PublicKeyFromPEM(senderPublicKeyPEM)
	if err != nil || publicKey.Type != anp.KeyTypeEd25519 {
		return nil, errors.New("발신 장치의 서명 공개키가 올바르지 않습니다")
	}
	expectedKeyID := benchmarkSyncV2KeyID("signing", publicKey)
	if subtle.ConstantTimeCompare([]byte(envelope.SenderKeyID), []byte(expectedKeyID)) != 1 {
		return nil, errors.New("발신 장치 키 식별자가 일치하지 않습니다")
	}
	signature, err := anp.DecodeBase64URL(envelope.Signature)
	if err != nil {
		return nil, errors.New("v2 메시지 서명 형식이 올바르지 않습니다")
	}
	signatureBase, err := benchmarkSyncV2EnvelopeSignatureBase(envelope)
	if err != nil {
		return nil, err
	}
	if err := publicKey.VerifyMessage(signatureBase, signature); err != nil {
		return nil, errors.New("v2 메시지 서명을 검증할 수 없습니다")
	}
	return append([]byte(nil), envelope.Payload...), nil
}

func benchmarkSyncV2EnvelopeSignatureBase(envelope benchmarkSyncV2SignedEnvelope) ([]byte, error) {
	return json.Marshal(benchmarkSyncV2SignatureInput{
		Version:       envelope.Version,
		Type:          envelope.Type,
		SenderKeyID:   envelope.SenderKeyID,
		CreatedAt:     envelope.CreatedAt,
		Nonce:         envelope.Nonce,
		PayloadSHA256: envelope.PayloadSHA256,
	})
}

func newBenchmarkSyncV2Nonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("v2 메시지 nonce를 만들 수 없습니다: %w", err)
	}
	return anp.EncodeBase64URL(value), nil
}
