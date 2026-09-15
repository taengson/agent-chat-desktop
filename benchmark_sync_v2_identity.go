package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	anp "github.com/agent-network-protocol/anp/golang"
	"github.com/zalando/go-keyring"
)

const (
	benchmarkSyncV2KeyringService  = "com.agentchat.desktop.benchmark-sync.v2"
	benchmarkSyncV2KeyringAccount  = "device-identity"
	benchmarkSyncV2IdentityVersion = 1
)

// BenchmarkSyncV2Identity contains only public identity information. The
// corresponding private keys are kept in the operating system's secret store
// and are never returned to the frontend or written to the app data folder.
type BenchmarkSyncV2Identity struct {
	Version                 int    `json:"version"`
	CreatedAt               string `json:"createdAt"`
	SigningKeyID            string `json:"signingKeyID"`
	SigningFingerprint      string `json:"signingFingerprint"`
	KeyAgreementKeyID       string `json:"keyAgreementKeyID"`
	KeyAgreementFingerprint string `json:"keyAgreementFingerprint"`
}

// benchmarkSyncV2SecretStore isolates platform credentials from the protocol
// code. Tests use an in-memory implementation; production uses the native
// Keychain, Credential Manager, or Secret Service through go-keyring.
type benchmarkSyncV2SecretStore interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
}

type benchmarkSyncV2SystemSecretStore struct{}

func (benchmarkSyncV2SystemSecretStore) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func (benchmarkSyncV2SystemSecretStore) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

// benchmarkSyncV2StoredIdentity is serialized only inside the OS secret
// store. Do not add this type to a JSON state file.
type benchmarkSyncV2StoredIdentity struct {
	Version                   int    `json:"version"`
	CreatedAt                 string `json:"createdAt"`
	SigningPrivateKeyPEM      string `json:"signingPrivateKeyPEM"`
	KeyAgreementPrivateKeyPEM string `json:"keyAgreementPrivateKeyPEM"`
}

type benchmarkSyncV2KeySet struct {
	publicIdentity   BenchmarkSyncV2Identity
	signingPrivate   anp.PrivateKeyMaterial
	signingPublic    anp.PublicKeyMaterial
	agreementPrivate anp.PrivateKeyMaterial
	agreementPublic  anp.PublicKeyMaterial
}

type benchmarkSyncV2IdentityStore struct {
	secrets benchmarkSyncV2SecretStore
	now     func() time.Time

	mu     sync.Mutex
	cached *benchmarkSyncV2KeySet
}

func newBenchmarkSyncV2IdentityStore(secrets benchmarkSyncV2SecretStore) *benchmarkSyncV2IdentityStore {
	return &benchmarkSyncV2IdentityStore{
		secrets: secrets,
		now:     time.Now,
	}
}

func (s *benchmarkSyncV2IdentityStore) LoadOrCreate() (benchmarkSyncV2KeySet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return *s.cached, nil
	}
	if s.secrets == nil {
		return benchmarkSyncV2KeySet{}, errors.New("v2 장치 키의 비밀 저장소가 설정되지 않았습니다")
	}

	stored, err := s.secrets.Get(benchmarkSyncV2KeyringService, benchmarkSyncV2KeyringAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return s.createLocked()
	}
	if err != nil {
		return benchmarkSyncV2KeySet{}, fmt.Errorf("운영체제 비밀 저장소에서 v2 장치 키를 읽을 수 없습니다: %w", err)
	}
	keySet, err := benchmarkSyncV2KeySetFromStored(stored)
	if err != nil {
		return benchmarkSyncV2KeySet{}, fmt.Errorf("저장된 v2 장치 키를 사용할 수 없습니다. 키를 재설정한 뒤 다시 페어링해야 합니다: %w", err)
	}
	s.cached = &keySet
	return keySet, nil
}

func (s *benchmarkSyncV2IdentityStore) createLocked() (benchmarkSyncV2KeySet, error) {
	signingPrivate, err := anp.GeneratePrivateKeyMaterial(anp.KeyTypeEd25519)
	if err != nil {
		return benchmarkSyncV2KeySet{}, fmt.Errorf("v2 서명 키를 만들 수 없습니다: %w", err)
	}
	agreementPrivate, err := anp.GeneratePrivateKeyMaterial(anp.KeyTypeX25519)
	if err != nil {
		return benchmarkSyncV2KeySet{}, fmt.Errorf("v2 암호화 키를 만들 수 없습니다: %w", err)
	}
	createdAt := s.now().UTC().Format(time.RFC3339Nano)
	stored, err := json.Marshal(benchmarkSyncV2StoredIdentity{
		Version:                   benchmarkSyncV2IdentityVersion,
		CreatedAt:                 createdAt,
		SigningPrivateKeyPEM:      signingPrivate.ToPEM(),
		KeyAgreementPrivateKeyPEM: agreementPrivate.ToPEM(),
	})
	if err != nil {
		return benchmarkSyncV2KeySet{}, fmt.Errorf("v2 장치 키를 준비할 수 없습니다: %w", err)
	}
	if err := s.secrets.Set(benchmarkSyncV2KeyringService, benchmarkSyncV2KeyringAccount, string(stored)); err != nil {
		return benchmarkSyncV2KeySet{}, fmt.Errorf("운영체제 비밀 저장소에 v2 장치 키를 저장할 수 없습니다: %w", err)
	}
	keySet, err := newBenchmarkSyncV2KeySet(createdAt, signingPrivate, agreementPrivate)
	if err != nil {
		return benchmarkSyncV2KeySet{}, err
	}
	s.cached = &keySet
	return keySet, nil
}

func benchmarkSyncV2KeySetFromStored(value string) (benchmarkSyncV2KeySet, error) {
	var stored benchmarkSyncV2StoredIdentity
	if err := json.Unmarshal([]byte(value), &stored); err != nil {
		return benchmarkSyncV2KeySet{}, errors.New("키 데이터 형식이 올바르지 않습니다")
	}
	if stored.Version != benchmarkSyncV2IdentityVersion || strings.TrimSpace(stored.CreatedAt) == "" {
		return benchmarkSyncV2KeySet{}, errors.New("지원하지 않는 키 데이터 버전입니다")
	}
	if _, err := time.Parse(time.RFC3339Nano, stored.CreatedAt); err != nil {
		return benchmarkSyncV2KeySet{}, errors.New("키 생성 시각이 올바르지 않습니다")
	}
	signingPrivate, err := anp.PrivateKeyFromPEM(stored.SigningPrivateKeyPEM)
	if err != nil || signingPrivate.Type != anp.KeyTypeEd25519 {
		return benchmarkSyncV2KeySet{}, errors.New("서명 개인키가 올바르지 않습니다")
	}
	agreementPrivate, err := anp.PrivateKeyFromPEM(stored.KeyAgreementPrivateKeyPEM)
	if err != nil || agreementPrivate.Type != anp.KeyTypeX25519 {
		return benchmarkSyncV2KeySet{}, errors.New("암호화 개인키가 올바르지 않습니다")
	}
	return newBenchmarkSyncV2KeySet(stored.CreatedAt, signingPrivate, agreementPrivate)
}

func newBenchmarkSyncV2KeySet(createdAt string, signingPrivate, agreementPrivate anp.PrivateKeyMaterial) (benchmarkSyncV2KeySet, error) {
	signingPublic, err := signingPrivate.PublicKey()
	if err != nil || signingPublic.Type != anp.KeyTypeEd25519 {
		return benchmarkSyncV2KeySet{}, errors.New("서명 공개키를 만들 수 없습니다")
	}
	agreementPublic, err := agreementPrivate.PublicKey()
	if err != nil || agreementPublic.Type != anp.KeyTypeX25519 {
		return benchmarkSyncV2KeySet{}, errors.New("암호화 공개키를 만들 수 없습니다")
	}
	signingKeyID := benchmarkSyncV2KeyID("signing", signingPublic)
	agreementKeyID := benchmarkSyncV2KeyID("agreement", agreementPublic)
	return benchmarkSyncV2KeySet{
		publicIdentity: BenchmarkSyncV2Identity{
			Version:                 benchmarkSyncV2IdentityVersion,
			CreatedAt:               createdAt,
			SigningKeyID:            signingKeyID,
			SigningFingerprint:      benchmarkSyncV2Fingerprint(signingPublic),
			KeyAgreementKeyID:       agreementKeyID,
			KeyAgreementFingerprint: benchmarkSyncV2Fingerprint(agreementPublic),
		},
		signingPrivate:   signingPrivate,
		signingPublic:    signingPublic,
		agreementPrivate: agreementPrivate,
		agreementPublic:  agreementPublic,
	}, nil
}

func benchmarkSyncV2KeyID(purpose string, key anp.PublicKeyMaterial) string {
	digest := sha256.Sum256(append([]byte(key.Type+":"), key.Bytes...))
	return "anp:" + purpose + ":" + anp.EncodeBase64URL(digest[:16])
}

func benchmarkSyncV2Fingerprint(key anp.PublicKeyMaterial) string {
	digest := sha256.Sum256(append([]byte(key.Type+":"), key.Bytes...))
	return "SHA256:" + anp.EncodeBase64URL(digest[:])
}
