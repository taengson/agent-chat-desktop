package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	anp "github.com/agent-network-protocol/anp/golang"
)

const benchmarkSyncV2RecordProofVersion = 1

// BenchmarkSyncV2RecordProof travels with a completed benchmark. It contains
// no private key and lets a trusted peer, or an imported report with its
// public key, detect modifications to the signed benchmark fields.
type BenchmarkSyncV2RecordProof struct {
	Version             int    `json:"version"`
	Type                string `json:"type"`
	KeyID               string `json:"keyID"`
	OriginBenchmarkID   string `json:"originBenchmarkID"`
	SignedAt            string `json:"signedAt"`
	PayloadSHA256       string `json:"payloadSHA256"`
	SigningPublicKeyPEM string `json:"signingPublicKeyPEM"`
	Signature           string `json:"signature"`
}

type benchmarkSyncV2RecordProofPayload struct {
	Version           int            `json:"version"`
	OriginBenchmarkID string         `json:"originBenchmarkID"`
	Record            ModelBenchmark `json:"record"`
}

type benchmarkSyncV2RecordProofSignatureInput struct {
	Version           int    `json:"version"`
	Type              string `json:"type"`
	KeyID             string `json:"keyID"`
	OriginBenchmarkID string `json:"originBenchmarkID"`
	SignedAt          string `json:"signedAt"`
	PayloadSHA256     string `json:"payloadSHA256"`
}

func signBenchmarkSyncV2Record(keySet benchmarkSyncV2KeySet, benchmark ModelBenchmark, now time.Time) (ModelBenchmark, error) {
	benchmark = normalizeModelBenchmark(benchmark)
	if benchmark.Status != "completed" {
		return ModelBenchmark{}, errors.New("완료된 벤치마크 결과만 v2 proof를 만들 수 있습니다")
	}
	if err := validateModelBenchmark(benchmark); err != nil {
		return ModelBenchmark{}, fmt.Errorf("v2 proof를 만들 벤치마크가 올바르지 않습니다: %w", err)
	}
	originBenchmarkID := benchmark.OriginBenchmarkID
	if originBenchmarkID == "" {
		originBenchmarkID = benchmark.ID
	}
	payload, err := benchmarkSyncV2RecordProofPayloadBytes(benchmark, originBenchmarkID)
	if err != nil {
		return ModelBenchmark{}, err
	}
	digest := sha256.Sum256(payload)
	proof := BenchmarkSyncV2RecordProof{
		Version:             benchmarkSyncV2RecordProofVersion,
		Type:                "anp-ed25519-record-proof-v1",
		KeyID:               keySet.publicIdentity.SigningKeyID,
		OriginBenchmarkID:   originBenchmarkID,
		SignedAt:            now.UTC().Format(time.RFC3339Nano),
		PayloadSHA256:       anp.EncodeBase64URL(digest[:]),
		SigningPublicKeyPEM: keySet.signingPublic.ToPEM(),
	}
	signatureBase, err := benchmarkSyncV2RecordProofSignatureBase(proof)
	if err != nil {
		return ModelBenchmark{}, err
	}
	signature, err := keySet.signingPrivate.SignMessage(signatureBase)
	if err != nil {
		return ModelBenchmark{}, fmt.Errorf("v2 결과 proof에 서명할 수 없습니다: %w", err)
	}
	proof.Signature = anp.EncodeBase64URL(signature)
	benchmark.Proof = &proof
	return benchmark, nil
}

// verifyBenchmarkSyncV2RecordProof verifies a result proof. expectedPublicKey
// is optional for portable reports, but a v2 peer sync must supply its pinned
// peer signing key so that an arbitrary embedded public key is not trusted.
func verifyBenchmarkSyncV2RecordProof(benchmark ModelBenchmark, expectedPublicKeyPEM string) error {
	benchmark = normalizeModelBenchmark(benchmark)
	proof := benchmark.Proof
	if proof == nil {
		return errors.New("v2 결과 proof가 없습니다")
	}
	if proof.Version != benchmarkSyncV2RecordProofVersion || proof.Type != "anp-ed25519-record-proof-v1" || proof.OriginBenchmarkID == "" || proof.SignedAt == "" {
		return errors.New("지원하지 않는 v2 결과 proof입니다")
	}
	if _, err := time.Parse(time.RFC3339Nano, proof.SignedAt); err != nil {
		return errors.New("v2 결과 proof의 서명 시각이 올바르지 않습니다")
	}
	if benchmark.OriginBenchmarkID != "" && benchmark.OriginBenchmarkID != proof.OriginBenchmarkID {
		return errors.New("v2 결과 proof의 원본 ID가 일치하지 않습니다")
	}
	if benchmark.Source == benchmarkSourceLocal && benchmark.ID != proof.OriginBenchmarkID {
		return errors.New("로컬 v2 결과 proof의 원본 ID가 일치하지 않습니다")
	}
	publicKey, err := anp.PublicKeyFromPEM(proof.SigningPublicKeyPEM)
	if err != nil || publicKey.Type != anp.KeyTypeEd25519 {
		return errors.New("v2 결과 proof의 서명 공개키가 올바르지 않습니다")
	}
	if expectedPublicKeyPEM != "" {
		expectedKey, expectedErr := anp.PublicKeyFromPEM(expectedPublicKeyPEM)
		if expectedErr != nil || expectedKey.Type != anp.KeyTypeEd25519 || subtle.ConstantTimeCompare(expectedKey.Bytes, publicKey.Bytes) != 1 {
			return errors.New("v2 결과 proof의 공개키가 신뢰한 상대 장치 키와 다릅니다")
		}
	}
	if subtle.ConstantTimeCompare([]byte(proof.KeyID), []byte(benchmarkSyncV2KeyID("signing", publicKey))) != 1 {
		return errors.New("v2 결과 proof의 키 식별자가 일치하지 않습니다")
	}
	payload, err := benchmarkSyncV2RecordProofPayloadBytes(benchmark, proof.OriginBenchmarkID)
	if err != nil {
		return err
	}
	expectedDigest := sha256.Sum256(payload)
	providedDigest, err := anp.DecodeBase64URL(proof.PayloadSHA256)
	if err != nil || len(providedDigest) != sha256.Size || subtle.ConstantTimeCompare(providedDigest, expectedDigest[:]) != 1 {
		return errors.New("v2 결과 proof와 벤치마크 내용이 일치하지 않습니다")
	}
	signature, err := anp.DecodeBase64URL(proof.Signature)
	if err != nil {
		return errors.New("v2 결과 proof의 서명 형식이 올바르지 않습니다")
	}
	signatureBase, err := benchmarkSyncV2RecordProofSignatureBase(*proof)
	if err != nil {
		return err
	}
	if err := publicKey.VerifyMessage(signatureBase, signature); err != nil {
		return errors.New("v2 결과 proof의 서명을 검증할 수 없습니다")
	}
	return nil
}

func benchmarkSyncV2RecordProofPayloadBytes(benchmark ModelBenchmark, originBenchmarkID string) ([]byte, error) {
	canonical := normalizeModelBenchmark(benchmark)
	canonical.ID = ""
	canonical.Imported = false
	canonical.Source = ""
	canonical.OriginDeviceID = ""
	canonical.OriginDeviceName = ""
	canonical.OriginBenchmarkID = ""
	canonical.Proof = nil
	payload, err := json.Marshal(benchmarkSyncV2RecordProofPayload{
		Version:           benchmarkSyncV2RecordProofVersion,
		OriginBenchmarkID: originBenchmarkID,
		Record:            canonical,
	})
	if err != nil {
		return nil, fmt.Errorf("v2 결과 proof 본문을 만들 수 없습니다: %w", err)
	}
	return payload, nil
}

func benchmarkSyncV2RecordProofSignatureBase(proof BenchmarkSyncV2RecordProof) ([]byte, error) {
	return json.Marshal(benchmarkSyncV2RecordProofSignatureInput{
		Version:           proof.Version,
		Type:              proof.Type,
		KeyID:             proof.KeyID,
		OriginBenchmarkID: proof.OriginBenchmarkID,
		SignedAt:          proof.SignedAt,
		PayloadSHA256:     proof.PayloadSHA256,
	})
}
