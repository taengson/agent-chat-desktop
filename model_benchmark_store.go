package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const modelBenchmarkDirectory = "model-benchmarks"

const (
	benchmarkSourceLocal  = "local"
	benchmarkSourceReport = "report"
	benchmarkSourceSync   = "sync"
)

var modelBenchmarkMarker = regexp.MustCompile(`(?m)^<!-- agent-chat-model-benchmark (\{.*\}) -->$`)

type ModelBenchmarkCase struct {
	ID       string           `json:"id"`
	Category string           `json:"category"`
	Title    string           `json:"title"`
	Prompt   string           `json:"prompt"`
	Content  string           `json:"content"`
	Status   string           `json:"status"`
	Usage    *TokenUsage      `json:"usage,omitempty"`
	Metrics  *ResponseMetrics `json:"metrics,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type ModelBenchmark struct {
	ID       string `json:"id"`
	Imported bool   `json:"imported"`
	// Source keeps the record's local provenance. Imported is retained for
	// compatibility with reports created by older versions of the app.
	Source            string                      `json:"source,omitempty"`
	OriginDeviceID    string                      `json:"originDeviceID,omitempty"`
	OriginDeviceName  string                      `json:"originDeviceName,omitempty"`
	OriginBenchmarkID string                      `json:"originBenchmarkID,omitempty"`
	ProfileID         string                      `json:"profileID"`
	ProfileName       string                      `json:"profileName"`
	ProfileBaseURL    string                      `json:"profileBaseURL"`
	Model             string                      `json:"model"`
	ReasoningEffort   string                      `json:"reasoningEffort,omitempty"`
	SuiteName         string                      `json:"suiteName"`
	Status            string                      `json:"status"`
	CreatedAt         string                      `json:"createdAt"`
	UpdatedAt         string                      `json:"updatedAt"`
	Cases             []ModelBenchmarkCase        `json:"cases"`
	Proof             *BenchmarkSyncV2RecordProof `json:"proof,omitempty"`
}

type ModelBenchmarkSummary struct {
	ID                          string  `json:"id"`
	Imported                    bool    `json:"imported"`
	Source                      string  `json:"source"`
	OriginDeviceName            string  `json:"originDeviceName,omitempty"`
	SuiteName                   string  `json:"suiteName"`
	Model                       string  `json:"model"`
	ReasoningEffort             string  `json:"reasoningEffort,omitempty"`
	ProfileName                 string  `json:"profileName"`
	ProfileBaseURL              string  `json:"profileBaseURL"`
	Status                      string  `json:"status"`
	UpdatedAt                   string  `json:"updatedAt"`
	CaseCount                   int     `json:"caseCount"`
	CompletedCaseCount          int     `json:"completedCaseCount"`
	AverageTotalDurationMs      int64   `json:"averageTotalDurationMs"`
	AverageFirstTokenDurationMs int64   `json:"averageFirstTokenDurationMs"`
	AverageGenerationSpeed      float64 `json:"averageGenerationSpeed"`
}

type modelBenchmarkStore struct {
	root string
	mu   sync.Mutex
}

func newModelBenchmarkStore(root string) *modelBenchmarkStore {
	return &modelBenchmarkStore{root: root}
}

func (s *modelBenchmarkStore) Create(benchmark ModelBenchmark) (ModelBenchmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	benchmark = normalizeModelBenchmark(benchmark)
	benchmark.Imported = false
	benchmark.Source = benchmarkSourceLocal
	benchmark.OriginDeviceID = ""
	benchmark.OriginDeviceName = ""
	benchmark.OriginBenchmarkID = ""
	benchmark.Proof = nil
	if benchmark.ID == "" {
		benchmark.ID = newConversationID()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	benchmark.CreatedAt = now
	benchmark.UpdatedAt = now
	if err := validateModelBenchmark(benchmark); err != nil {
		return ModelBenchmark{}, err
	}
	if err := s.saveLocked(benchmark); err != nil {
		return ModelBenchmark{}, err
	}
	return benchmark, nil
}

func (s *modelBenchmarkStore) Save(benchmark ModelBenchmark) (ModelBenchmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	benchmark = normalizeModelBenchmark(benchmark)
	if benchmark.Source != benchmarkSourceLocal {
		return ModelBenchmark{}, errors.New("가져온 벤치마크 결과는 수정할 수 없습니다")
	}
	// Local edits invalidate an earlier proof. v2 creates a replacement proof
	// only when the record is prepared for a trusted secure transfer.
	benchmark.Proof = nil
	if benchmark.CreatedAt == "" {
		return ModelBenchmark{}, errors.New("벤치마크 생성 시간이 없습니다")
	}
	benchmark.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := validateModelBenchmark(benchmark); err != nil {
		return ModelBenchmark{}, err
	}
	if err := s.saveLocked(benchmark); err != nil {
		return ModelBenchmark{}, err
	}
	return benchmark, nil
}

// SaveV2Proof stores a proof that was created from the record's current
// completed contents. It intentionally does not change UpdatedAt: that field
// is included in the proof and changing it after signing would invalidate the
// record. Ordinary Save calls clear this proof when a local record is edited.
func (s *modelBenchmarkStore) SaveV2Proof(benchmark ModelBenchmark) (ModelBenchmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	benchmark = normalizeModelBenchmark(benchmark)
	if benchmark.Source != benchmarkSourceLocal {
		return ModelBenchmark{}, errors.New("가져온 벤치마크에는 로컬 v2 proof를 저장할 수 없습니다")
	}
	if benchmark.Proof == nil {
		return ModelBenchmark{}, errors.New("저장할 v2 결과 proof가 없습니다")
	}
	if err := validateModelBenchmark(benchmark); err != nil {
		return ModelBenchmark{}, err
	}
	if err := verifyBenchmarkSyncV2RecordProof(benchmark, ""); err != nil {
		return ModelBenchmark{}, fmt.Errorf("저장할 v2 결과 proof가 올바르지 않습니다: %w", err)
	}
	if err := s.saveLocked(benchmark); err != nil {
		return ModelBenchmark{}, err
	}
	return benchmark, nil
}

func (s *modelBenchmarkStore) Open(id string) (ModelBenchmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !isSafeConversationID(id) {
		return ModelBenchmark{}, errors.New("올바르지 않은 벤치마크 ID입니다")
	}
	directory, err := s.directory()
	if err != nil {
		return ModelBenchmark{}, err
	}
	contents, err := os.ReadFile(filepath.Join(directory, id+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return ModelBenchmark{}, errors.New("벤치마크 기록을 찾을 수 없습니다")
	}
	if err != nil {
		return ModelBenchmark{}, fmt.Errorf("벤치마크 기록을 읽을 수 없습니다: %w", err)
	}
	benchmark, err := parseModelBenchmark(contents)
	if err != nil {
		return ModelBenchmark{}, fmt.Errorf("벤치마크 기록 형식이 올바르지 않습니다: %w", err)
	}
	if benchmark.ID != id {
		return ModelBenchmark{}, errors.New("벤치마크 기록 ID가 일치하지 않습니다")
	}
	return benchmark, nil
}

func (s *modelBenchmarkStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !isSafeConversationID(id) {
		return errors.New("올바르지 않은 벤치마크 ID입니다")
	}
	directory, err := s.directory()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, id+".md")); errors.Is(err, os.ErrNotExist) {
		return errors.New("벤치마크 기록을 찾을 수 없습니다")
	} else if err != nil {
		return fmt.Errorf("벤치마크 기록을 삭제할 수 없습니다: %w", err)
	}
	return nil
}

func (s *modelBenchmarkStore) List() ([]ModelBenchmarkSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	directory, err := s.directory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("벤치마크 목록을 읽을 수 없습니다: %w", err)
	}

	summaries := make([]ModelBenchmarkSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("벤치마크 기록을 읽을 수 없습니다: %w", err)
		}
		benchmark, err := parseModelBenchmark(contents)
		if err != nil {
			return nil, fmt.Errorf("벤치마크 기록 %q의 형식이 올바르지 않습니다: %w", entry.Name(), err)
		}
		summaries = append(summaries, modelBenchmarkSummary(benchmark))
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	return summaries, nil
}

func (s *modelBenchmarkStore) saveLocked(benchmark ModelBenchmark) error {
	directory, err := s.directory()
	if err != nil {
		return err
	}
	contents, err := marshalModelBenchmark(benchmark)
	if err != nil {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".model-benchmark-*")
	if err != nil {
		return fmt.Errorf("벤치마크 임시 파일을 만들 수 없습니다: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("벤치마크 파일 권한을 설정할 수 없습니다: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("벤치마크 기록을 저장할 수 없습니다: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("벤치마크 기록을 저장할 수 없습니다: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, benchmark.ID+".md")); err != nil {
		return fmt.Errorf("벤치마크 기록을 교체할 수 없습니다: %w", err)
	}
	return nil
}

func (s *modelBenchmarkStore) directory() (string, error) {
	root, err := applicationDataDirectory(s.root)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(root, modelBenchmarkDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("벤치마크 저장 폴더를 만들 수 없습니다: %w", err)
	}
	return directory, nil
}

func normalizeModelBenchmark(benchmark ModelBenchmark) ModelBenchmark {
	benchmark.ID = strings.TrimSpace(benchmark.ID)
	benchmark.Source = strings.TrimSpace(strings.ToLower(benchmark.Source))
	if benchmark.Source == "" {
		if benchmark.Imported {
			benchmark.Source = benchmarkSourceReport
		} else {
			benchmark.Source = benchmarkSourceLocal
		}
	}
	benchmark.OriginDeviceID = strings.TrimSpace(benchmark.OriginDeviceID)
	benchmark.OriginDeviceName = normalizeProfileName(benchmark.OriginDeviceName)
	benchmark.OriginBenchmarkID = strings.TrimSpace(benchmark.OriginBenchmarkID)
	if benchmark.Proof != nil {
		proof := *benchmark.Proof
		proof.KeyID = strings.TrimSpace(proof.KeyID)
		proof.OriginBenchmarkID = strings.TrimSpace(proof.OriginBenchmarkID)
		proof.SignedAt = strings.TrimSpace(proof.SignedAt)
		proof.PayloadSHA256 = strings.TrimSpace(proof.PayloadSHA256)
		proof.Signature = strings.TrimSpace(proof.Signature)
		proof.SigningPublicKeyPEM = strings.TrimSpace(proof.SigningPublicKeyPEM)
		benchmark.Proof = &proof
	}
	benchmark.Imported = benchmark.Source != benchmarkSourceLocal
	benchmark.ProfileID = strings.TrimSpace(benchmark.ProfileID)
	benchmark.ProfileName = normalizeProfileName(benchmark.ProfileName)
	benchmark.ProfileBaseURL = strings.TrimSpace(benchmark.ProfileBaseURL)
	benchmark.Model = strings.TrimSpace(benchmark.Model)
	benchmark.ReasoningEffort = strings.TrimSpace(benchmark.ReasoningEffort)
	benchmark.SuiteName = strings.TrimSpace(benchmark.SuiteName)
	benchmark.Status = strings.TrimSpace(benchmark.Status)
	for index := range benchmark.Cases {
		benchmark.Cases[index].ID = strings.TrimSpace(benchmark.Cases[index].ID)
		benchmark.Cases[index].Category = strings.TrimSpace(benchmark.Cases[index].Category)
		benchmark.Cases[index].Title = strings.TrimSpace(benchmark.Cases[index].Title)
		benchmark.Cases[index].Prompt = strings.TrimSpace(benchmark.Cases[index].Prompt)
		benchmark.Cases[index].Status = strings.TrimSpace(benchmark.Cases[index].Status)
	}
	return benchmark
}

func validateModelBenchmark(benchmark ModelBenchmark) error {
	if !isSafeConversationID(benchmark.ID) {
		return errors.New("올바르지 않은 벤치마크 ID입니다")
	}
	if benchmark.Source != benchmarkSourceLocal && benchmark.Source != benchmarkSourceReport && benchmark.Source != benchmarkSourceSync {
		return errors.New("올바르지 않은 벤치마크 출처입니다")
	}
	if benchmark.Source == benchmarkSourceSync {
		if benchmark.OriginDeviceID == "" || benchmark.OriginBenchmarkID == "" {
			return errors.New("동기화된 벤치마크의 원본 정보가 없습니다")
		}
	}
	if !isSafeConnectionProfileID(benchmark.ProfileID) || benchmark.ProfileName == "" {
		return errors.New("저장된 연결 프로필을 선택해 주세요")
	}
	if err := validateSavedConnectionProfile(SavedConnectionProfile{BaseURL: benchmark.ProfileBaseURL}); err != nil {
		return err
	}
	if benchmark.Model == "" || len([]rune(benchmark.Model)) > 512 {
		return errors.New("올바른 모델을 선택해 주세요")
	}
	if _, err := normalizeReasoningEffort(benchmark.ReasoningEffort); err != nil {
		return err
	}
	if benchmark.SuiteName == "" || len([]rune(benchmark.SuiteName)) > 120 {
		return errors.New("올바른 벤치마크 이름이 필요합니다")
	}
	if benchmark.CreatedAt == "" || benchmark.UpdatedAt == "" {
		return errors.New("벤치마크 시간이 없습니다")
	}
	if benchmark.Status != "running" && benchmark.Status != "completed" {
		return errors.New("올바르지 않은 벤치마크 상태입니다")
	}
	if len(benchmark.Cases) < 1 || len(benchmark.Cases) > 12 {
		return errors.New("벤치마크에는 1개에서 12개 테스트 항목이 필요합니다")
	}

	caseIDs := make(map[string]struct{}, len(benchmark.Cases))
	for _, benchmarkCase := range benchmark.Cases {
		if !isSafeConversationID(benchmarkCase.ID) || benchmarkCase.Category == "" || benchmarkCase.Title == "" || benchmarkCase.Prompt == "" || len([]rune(benchmarkCase.Prompt)) > 100_000 {
			return errors.New("올바르지 않은 벤치마크 테스트 항목입니다")
		}
		if _, exists := caseIDs[benchmarkCase.ID]; exists {
			return errors.New("중복된 벤치마크 테스트 항목입니다")
		}
		if benchmarkCase.Status != "pending" && benchmarkCase.Status != "streaming" && benchmarkCase.Status != "complete" && benchmarkCase.Status != "cancelled" && benchmarkCase.Status != "failed" {
			return errors.New("올바르지 않은 테스트 응답 상태입니다")
		}
		caseIDs[benchmarkCase.ID] = struct{}{}
	}
	if benchmark.Status == "completed" {
		for _, benchmarkCase := range benchmark.Cases {
			if benchmarkCase.Status == "pending" || benchmarkCase.Status == "streaming" {
				return errors.New("완료된 벤치마크에는 미실행 테스트가 있을 수 없습니다")
			}
		}
	}
	return nil
}

func marshalModelBenchmark(benchmark ModelBenchmark) ([]byte, error) {
	payload, err := json.Marshal(benchmark)
	if err != nil {
		return nil, fmt.Errorf("벤치마크 정보를 저장할 수 없습니다: %w", err)
	}
	var builder strings.Builder
	builder.WriteString("---\nid: ")
	builder.WriteString(benchmark.ID)
	builder.WriteString("\ncreated_at: ")
	builder.WriteString(benchmark.CreatedAt)
	builder.WriteString("\nupdated_at: ")
	builder.WriteString(benchmark.UpdatedAt)
	builder.WriteString("\nstatus: ")
	builder.WriteString(benchmark.Status)
	builder.WriteString("\n---\n\n# ")
	builder.WriteString(modelBenchmarkTitle(benchmark))
	builder.WriteString("\n\n<!-- agent-chat-model-benchmark ")
	builder.Write(payload)
	builder.WriteString(" -->\n\n## 모델\n\n")
	builder.WriteString(benchmark.Model)
	builder.WriteString("\n\n## 연결 프로필\n\n")
	builder.WriteString(benchmark.ProfileName)
	builder.WriteString("\n\n")
	builder.WriteString(benchmark.ProfileBaseURL)
	builder.WriteString("\n\n## 추론 강도\n\n")
	if benchmark.ReasoningEffort == "" {
		builder.WriteString("자동 (서버 기본값)")
	} else {
		builder.WriteString(benchmark.ReasoningEffort)
	}
	builder.WriteString("\n\n## 테스트 항목\n")
	for _, benchmarkCase := range benchmark.Cases {
		builder.WriteString("\n### ")
		builder.WriteString(benchmarkCase.Category)
		builder.WriteString(" · ")
		builder.WriteString(benchmarkCase.Title)
		builder.WriteString("\n\n")
		builder.WriteString(benchmarkCase.Prompt)
		builder.WriteString("\n")
	}
	return []byte(builder.String()), nil
}

func parseModelBenchmark(contents []byte) (ModelBenchmark, error) {
	match := modelBenchmarkMarker.FindSubmatch(contents)
	if len(match) != 2 {
		return ModelBenchmark{}, errors.New("벤치마크 정보가 없습니다")
	}
	var benchmark ModelBenchmark
	if err := json.Unmarshal(match[1], &benchmark); err != nil {
		return ModelBenchmark{}, errors.New("벤치마크 정보가 올바르지 않습니다")
	}
	benchmark = normalizeModelBenchmark(benchmark)
	if err := validateModelBenchmark(benchmark); err != nil {
		return ModelBenchmark{}, err
	}
	return benchmark, nil
}

func modelBenchmarkTitle(benchmark ModelBenchmark) string {
	return truncateTitle(benchmark.SuiteName)
}

func modelBenchmarkSummary(benchmark ModelBenchmark) ModelBenchmarkSummary {
	summary := ModelBenchmarkSummary{
		ID:               benchmark.ID,
		Imported:         benchmark.Imported,
		Source:           benchmark.Source,
		OriginDeviceName: benchmark.OriginDeviceName,
		SuiteName:        modelBenchmarkTitle(benchmark),
		Model:            benchmark.Model,
		ReasoningEffort:  benchmark.ReasoningEffort,
		ProfileName:      benchmark.ProfileName,
		ProfileBaseURL:   benchmark.ProfileBaseURL,
		Status:           benchmark.Status,
		UpdatedAt:        benchmark.UpdatedAt,
		CaseCount:        len(benchmark.Cases),
	}
	var totalDuration int64
	var firstTokenDuration int64
	var metricCount int64
	var totalCompletionTokens int64
	var totalGenerationDuration int64
	for _, benchmarkCase := range benchmark.Cases {
		if benchmarkCase.Status == "complete" || benchmarkCase.Status == "cancelled" || benchmarkCase.Status == "failed" {
			summary.CompletedCaseCount++
		}
		if benchmarkCase.Metrics != nil {
			totalDuration += benchmarkCase.Metrics.TotalDurationMs
			firstTokenDuration += benchmarkCase.Metrics.FirstTokenDurationMs
			metricCount++
			generationDuration := benchmarkCase.Metrics.TotalDurationMs - benchmarkCase.Metrics.FirstTokenDurationMs
			if benchmarkCase.Usage != nil && benchmarkCase.Usage.CompletionTokens > 0 && generationDuration > 0 {
				totalCompletionTokens += int64(benchmarkCase.Usage.CompletionTokens)
				totalGenerationDuration += generationDuration
			}
		}
	}
	if metricCount > 0 {
		summary.AverageTotalDurationMs = totalDuration / metricCount
		summary.AverageFirstTokenDurationMs = firstTokenDuration / metricCount
	}
	if totalGenerationDuration > 0 {
		summary.AverageGenerationSpeed = float64(totalCompletionTokens) / (float64(totalGenerationDuration) / 1_000)
	}
	return summary
}
