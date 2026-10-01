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

const (
	agenticEvaluationDirectory      = "agentic-evaluations"
	maxStoredAgenticEvaluations     = 80
	maxAgenticEvaluationRecordBytes = 64 * 1024 * 1024
)

var agenticEvaluationMarker = regexp.MustCompile(`(?m)^<!-- agent-chat-agentic-evaluation (\{.*\}) -->$`)

// AgenticEvaluationStartRequest starts one sequential queue. Profile.APIKey is
// used only while the queue is running and is deliberately excluded from the
// saved AgenticEvaluation record.
type AgenticEvaluationStartRequest struct {
	Profile         ConnectionProfile `json:"profile"`
	ProfileID       string            `json:"profileID"`
	ProfileName     string            `json:"profileName"`
	ModelIDs        []string          `json:"modelIDs"`
	ScenarioIDs     []string          `json:"scenarioIDs"`
	Repetitions     int               `json:"repetitions"`
	ReasoningEffort string            `json:"reasoningEffort,omitempty"`
}

type AgenticEvaluation struct {
	ID              string                 `json:"id"`
	ProfileID       string                 `json:"profileID"`
	ProfileName     string                 `json:"profileName"`
	ProfileBaseURL  string                 `json:"profileBaseURL"`
	ModelIDs        []string               `json:"modelIDs"`
	ScenarioIDs     []string               `json:"scenarioIDs"`
	Repetitions     int                    `json:"repetitions"`
	ReasoningEffort string                 `json:"reasoningEffort,omitempty"`
	ExecutionRules  AgenticExecutionRules  `json:"executionRules"`
	Status          string                 `json:"status"`
	CreatedAt       string                 `json:"createdAt"`
	UpdatedAt       string                 `json:"updatedAt"`
	Runs            []AgenticEvaluationRun `json:"runs"`
}

// AgenticExecutionRules records the shared evaluation contract. It makes a
// stored result interpretable even after defaults evolve in a later release.
type AgenticExecutionRules struct {
	ActionFormatVersion   string `json:"actionFormatVersion"`
	SystemPromptVersion   string `json:"systemPromptVersion"`
	ToolDefinitionVersion string `json:"toolDefinitionVersion"`
	GraderVersion         string `json:"graderVersion"`
	MaxActions            int    `json:"maxActions"`
	MaxInvalidActions     int    `json:"maxInvalidActions"`
	ContextLimitBytes     int    `json:"contextLimitBytes"`
	ResponseLimitBytes    int    `json:"responseLimitBytes"`
	ToolOutputLimitBytes  int    `json:"toolOutputLimitBytes"`
	RunTimeoutSeconds     int    `json:"runTimeoutSeconds"`
	ActionTimeoutSeconds  int    `json:"actionTimeoutSeconds"`
}

type AgenticEvaluationRun struct {
	ID               string                    `json:"id"`
	Model            string                    `json:"model"`
	ScenarioID       string                    `json:"scenarioID"`
	ScenarioVersion  string                    `json:"scenarioVersion"`
	InitialStateHash string                    `json:"initialStateHash,omitempty"`
	GraderVersion    string                    `json:"graderVersion"`
	Environment      string                    `json:"environment"`
	Category         string                    `json:"category"`
	Title            string                    `json:"title"`
	Goal             string                    `json:"goal"`
	Variant          int                       `json:"variant"`
	Status           string                    `json:"status"`
	StartedAt        string                    `json:"startedAt,omitempty"`
	FinishedAt       string                    `json:"finishedAt,omitempty"`
	Actions          []AgenticEvaluationAction `json:"actions"`
	StateChanges     []AgenticEvaluationChange `json:"stateChanges,omitempty"`
	Result           *AgenticEvaluationResult  `json:"result,omitempty"`
	Usage            *TokenUsage               `json:"usage,omitempty"`
	Metrics          *ResponseMetrics          `json:"metrics,omitempty"`
	Error            string                    `json:"error,omitempty"`
}

type AgenticEvaluationAction struct {
	Step       int    `json:"step"`
	Type       string `json:"type"`
	ToolName   string `json:"toolName,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
	Output     string `json:"output,omitempty"`
	Status     string `json:"status"`
	RawContent string `json:"rawContent,omitempty"`
	OccurredAt string `json:"occurredAt"`
}

type AgenticEvaluationChange struct {
	Resource string `json:"resource"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
}

type AgenticEvaluationResult struct {
	Passed       bool     `json:"passed"`
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Requirements []string `json:"requirements,omitempty"`
	Violations   []string `json:"violations,omitempty"`
}

type AgenticEvaluationSummary struct {
	ID               string   `json:"id"`
	ProfileName      string   `json:"profileName"`
	ProfileBaseURL   string   `json:"profileBaseURL"`
	Models           []string `json:"models"`
	ScenarioCount    int      `json:"scenarioCount"`
	Repetitions      int      `json:"repetitions"`
	Status           string   `json:"status"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
	RunCount         int      `json:"runCount"`
	FinishedRunCount int      `json:"finishedRunCount"`
	PassedRunCount   int      `json:"passedRunCount"`
}

type agenticEvaluationStore struct {
	root string
	mu   sync.Mutex
}

func newAgenticEvaluationStore(root string) *agenticEvaluationStore {
	return &agenticEvaluationStore{root: root}
}

func (s *agenticEvaluationStore) Create(evaluation AgenticEvaluation) (AgenticEvaluation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	evaluation = normalizeAgenticEvaluation(evaluation)
	if evaluation.ID == "" {
		evaluation.ID = newConversationID()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	evaluation.CreatedAt = now
	evaluation.UpdatedAt = now
	if err := validateAgenticEvaluation(evaluation); err != nil {
		return AgenticEvaluation{}, err
	}
	if err := s.pruneCompletedLocked(); err != nil {
		return AgenticEvaluation{}, err
	}
	if err := s.saveLocked(evaluation); err != nil {
		return AgenticEvaluation{}, err
	}
	return evaluation, nil
}

func (s *agenticEvaluationStore) Save(evaluation AgenticEvaluation) (AgenticEvaluation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	evaluation = normalizeAgenticEvaluation(evaluation)
	if evaluation.CreatedAt == "" {
		return AgenticEvaluation{}, errors.New("에이전트 실험 생성 시간이 없습니다")
	}
	evaluation.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := validateAgenticEvaluation(evaluation); err != nil {
		return AgenticEvaluation{}, err
	}
	if err := s.saveLocked(evaluation); err != nil {
		return AgenticEvaluation{}, err
	}
	return evaluation, nil
}

func (s *agenticEvaluationStore) Open(id string) (AgenticEvaluation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !isSafeConversationID(id) {
		return AgenticEvaluation{}, errors.New("올바르지 않은 에이전트 실험 ID입니다")
	}
	directory, err := s.directory()
	if err != nil {
		return AgenticEvaluation{}, err
	}
	contents, err := os.ReadFile(filepath.Join(directory, id+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return AgenticEvaluation{}, errors.New("에이전트 실험 기록을 찾을 수 없습니다")
	}
	if err != nil {
		return AgenticEvaluation{}, fmt.Errorf("에이전트 실험 기록을 읽을 수 없습니다: %w", err)
	}
	evaluation, err := parseAgenticEvaluation(contents)
	if err != nil {
		return AgenticEvaluation{}, fmt.Errorf("에이전트 실험 기록 형식이 올바르지 않습니다: %w", err)
	}
	if evaluation.ID != id {
		return AgenticEvaluation{}, errors.New("에이전트 실험 기록 ID가 일치하지 않습니다")
	}
	return evaluation, nil
}

func (s *agenticEvaluationStore) List() ([]AgenticEvaluationSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	directory, err := s.directory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("에이전트 실험 목록을 읽을 수 없습니다: %w", err)
	}
	summaries := make([]AgenticEvaluationSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("에이전트 실험 기록을 읽을 수 없습니다: %w", err)
		}
		evaluation, err := parseAgenticEvaluation(contents)
		if err != nil {
			return nil, fmt.Errorf("에이전트 실험 기록 %q의 형식이 올바르지 않습니다: %w", entry.Name(), err)
		}
		summaries = append(summaries, agenticEvaluationSummary(evaluation))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].UpdatedAt > summaries[j].UpdatedAt })
	return summaries, nil
}

func (s *agenticEvaluationStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !isSafeConversationID(id) {
		return errors.New("올바르지 않은 에이전트 실험 ID입니다")
	}
	directory, err := s.directory()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, id+".md")); errors.Is(err, os.ErrNotExist) {
		return errors.New("에이전트 실험 기록을 찾을 수 없습니다")
	} else if err != nil {
		return fmt.Errorf("에이전트 실험 기록을 삭제할 수 없습니다: %w", err)
	}
	return nil
}

// pruneCompletedLocked retains bounded local history without touching a live
// queue. A new record is created only after the oldest completed records have
// been removed, so a full disk history cannot grow without limit.
func (s *agenticEvaluationStore) pruneCompletedLocked() error {
	directory, err := s.directory()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("에이전트 실험 목록을 읽을 수 없습니다: %w", err)
	}
	type storedRecord struct {
		path      string
		updatedAt string
	}
	completed := make([]storedRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("에이전트 실험 기록을 읽을 수 없습니다: %w", readErr)
		}
		evaluation, parseErr := parseAgenticEvaluation(contents)
		if parseErr != nil {
			return fmt.Errorf("에이전트 실험 기록 %q의 형식이 올바르지 않습니다: %w", entry.Name(), parseErr)
		}
		if evaluation.Status != "running" {
			completed = append(completed, storedRecord{path: path, updatedAt: evaluation.UpdatedAt})
		}
	}
	removeCount := len(completed) - maxStoredAgenticEvaluations + 1
	if removeCount <= 0 {
		return nil
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].updatedAt < completed[j].updatedAt })
	for _, record := range completed[:removeCount] {
		if err := os.Remove(record.path); err != nil {
			return fmt.Errorf("오래된 에이전트 실험 기록을 정리할 수 없습니다: %w", err)
		}
	}
	return nil
}

// MarkInterrupted closes records that were still running when the application
// stopped. Execution state lives only in memory, so continuing such a record
// could accidentally mix a fresh model conversation with an old environment.
func (s *agenticEvaluationStore) MarkInterrupted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := s.directory()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("에이전트 실험 목록을 읽을 수 없습니다: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return fmt.Errorf("에이전트 실험 기록을 읽을 수 없습니다: %w", err)
		}
		evaluation, err := parseAgenticEvaluation(contents)
		if err != nil {
			return fmt.Errorf("에이전트 실험 기록 %q의 형식이 올바르지 않습니다: %w", entry.Name(), err)
		}
		if evaluation.Status != "running" {
			continue
		}
		evaluation.Status = "cancelled"
		for runIndex := range evaluation.Runs {
			run := &evaluation.Runs[runIndex]
			if run.Status != "pending" && run.Status != "running" {
				continue
			}
			run.Status = "cancelled"
			run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			run.Result = &AgenticEvaluationResult{Passed: false, Outcome: "interrupted", Summary: "앱이 종료되어 실행을 이어 가지 않았습니다."}
			run.Error = "앱 종료로 중단됨"
		}
		evaluation.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.saveLocked(evaluation); err != nil {
			return err
		}
	}
	return nil
}

func (s *agenticEvaluationStore) directory() (string, error) {
	root, err := applicationDataDirectory(s.root)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(root, agenticEvaluationDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("에이전트 실험 저장 폴더를 만들 수 없습니다: %w", err)
	}
	return directory, nil
}

func (s *agenticEvaluationStore) saveLocked(evaluation AgenticEvaluation) error {
	directory, err := s.directory()
	if err != nil {
		return err
	}
	contents, err := marshalAgenticEvaluation(evaluation)
	if err != nil {
		return err
	}
	if len(contents) > maxAgenticEvaluationRecordBytes {
		return fmt.Errorf("에이전트 실험 상세 기록이 %dMB 제한을 넘었습니다", maxAgenticEvaluationRecordBytes/(1024*1024))
	}
	temporary, err := os.CreateTemp(directory, ".agentic-evaluation-*")
	if err != nil {
		return fmt.Errorf("에이전트 실험 임시 파일을 만들 수 없습니다: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("에이전트 실험 파일 권한을 설정할 수 없습니다: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("에이전트 실험 기록을 저장할 수 없습니다: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("에이전트 실험 기록을 저장할 수 없습니다: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, evaluation.ID+".md")); err != nil {
		return fmt.Errorf("에이전트 실험 기록을 교체할 수 없습니다: %w", err)
	}
	return nil
}

func normalizeAgenticEvaluation(evaluation AgenticEvaluation) AgenticEvaluation {
	evaluation.ID = strings.TrimSpace(evaluation.ID)
	evaluation.ProfileID = strings.TrimSpace(evaluation.ProfileID)
	evaluation.ProfileName = normalizeProfileName(evaluation.ProfileName)
	evaluation.ProfileBaseURL = strings.TrimSpace(evaluation.ProfileBaseURL)
	evaluation.ReasoningEffort = strings.TrimSpace(evaluation.ReasoningEffort)
	if evaluation.ExecutionRules.ActionFormatVersion == "" {
		evaluation.ExecutionRules = defaultAgenticExecutionRules()
	}
	evaluation.Status = strings.TrimSpace(evaluation.Status)
	evaluation.ModelIDs = normalizeAgenticStrings(evaluation.ModelIDs)
	evaluation.ScenarioIDs = normalizeAgenticStrings(evaluation.ScenarioIDs)
	for index := range evaluation.Runs {
		run := &evaluation.Runs[index]
		run.ID = strings.TrimSpace(run.ID)
		run.Model = strings.TrimSpace(run.Model)
		run.ScenarioID = strings.TrimSpace(run.ScenarioID)
		run.ScenarioVersion = strings.TrimSpace(run.ScenarioVersion)
		run.InitialStateHash = strings.TrimSpace(run.InitialStateHash)
		run.GraderVersion = strings.TrimSpace(run.GraderVersion)
		if run.GraderVersion == "" {
			run.GraderVersion = evaluation.ExecutionRules.GraderVersion
		}
		run.Environment = strings.TrimSpace(run.Environment)
		run.Category = strings.TrimSpace(run.Category)
		run.Title = strings.TrimSpace(run.Title)
		run.Goal = strings.TrimSpace(run.Goal)
		run.Status = strings.TrimSpace(run.Status)
		run.Error = strings.TrimSpace(run.Error)
		for actionIndex := range run.Actions {
			action := &run.Actions[actionIndex]
			action.Type = strings.TrimSpace(action.Type)
			action.ToolName = strings.TrimSpace(action.ToolName)
			action.Status = strings.TrimSpace(action.Status)
		}
	}
	return evaluation
}

func normalizeAgenticStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validateAgenticEvaluation(evaluation AgenticEvaluation) error {
	if !isSafeConversationID(evaluation.ID) {
		return errors.New("올바르지 않은 에이전트 실험 ID입니다")
	}
	if !isSafeConnectionProfileID(evaluation.ProfileID) || evaluation.ProfileName == "" {
		return errors.New("저장된 연결 프로필을 선택해 주세요")
	}
	if err := validateSavedConnectionProfile(SavedConnectionProfile{BaseURL: evaluation.ProfileBaseURL}); err != nil {
		return err
	}
	if len(evaluation.ModelIDs) < 1 || len(evaluation.ModelIDs) > 12 {
		return errors.New("1개에서 12개의 모델을 선택해 주세요")
	}
	for _, modelID := range evaluation.ModelIDs {
		if len([]rune(modelID)) > 512 {
			return errors.New("올바르지 않은 모델이 포함되어 있습니다")
		}
	}
	if len(evaluation.ScenarioIDs) < 1 || len(evaluation.ScenarioIDs) > 12 {
		return errors.New("1개에서 12개의 시나리오를 선택해 주세요")
	}
	if evaluation.Repetitions < 1 || evaluation.Repetitions > 10 {
		return errors.New("반복 횟수는 1에서 10 사이여야 합니다")
	}
	if _, err := normalizeReasoningEffort(evaluation.ReasoningEffort); err != nil {
		return err
	}
	if err := validateAgenticExecutionRules(evaluation.ExecutionRules); err != nil {
		return err
	}
	if evaluation.Status != "running" && evaluation.Status != "completed" && evaluation.Status != "cancelled" {
		return errors.New("올바르지 않은 에이전트 실험 상태입니다")
	}
	if evaluation.CreatedAt == "" || evaluation.UpdatedAt == "" {
		return errors.New("에이전트 실험 시간이 없습니다")
	}
	if len(evaluation.Runs) < 1 || len(evaluation.Runs) > maxAgenticEvaluationRuns {
		return errors.New("올바르지 않은 에이전트 실행 목록입니다")
	}
	seenRunIDs := make(map[string]struct{}, len(evaluation.Runs))
	for _, run := range evaluation.Runs {
		if !isSafeConversationID(run.ID) || run.Model == "" || run.ScenarioID == "" || run.ScenarioVersion == "" || run.GraderVersion == "" || run.Environment == "" || run.Title == "" || run.Goal == "" {
			return errors.New("올바르지 않은 에이전트 실행 기록입니다")
		}
		if _, exists := seenRunIDs[run.ID]; exists {
			return errors.New("중복된 에이전트 실행 기록입니다")
		}
		seenRunIDs[run.ID] = struct{}{}
		switch run.Status {
		case "pending", "running", "success", "failed", "cancelled", "connection_error", "action_limit", "time_limit", "context_limit":
		default:
			return errors.New("올바르지 않은 에이전트 실행 상태입니다")
		}
		if len(run.Actions) > 64 || len(run.StateChanges) > 32 {
			return errors.New("에이전트 실행 기록이 허용 범위를 벗어났습니다")
		}
	}
	return nil
}

func marshalAgenticEvaluation(evaluation AgenticEvaluation) ([]byte, error) {
	payload, err := json.Marshal(evaluation)
	if err != nil {
		return nil, fmt.Errorf("에이전트 실험 정보를 저장할 수 없습니다: %w", err)
	}
	var builder strings.Builder
	builder.WriteString("---\nid: ")
	builder.WriteString(evaluation.ID)
	builder.WriteString("\ncreated_at: ")
	builder.WriteString(evaluation.CreatedAt)
	builder.WriteString("\nupdated_at: ")
	builder.WriteString(evaluation.UpdatedAt)
	builder.WriteString("\nstatus: ")
	builder.WriteString(evaluation.Status)
	builder.WriteString("\n---\n\n# 에이전트 실험\n\n")
	builder.WriteString("<!-- agent-chat-agentic-evaluation ")
	builder.Write(payload)
	builder.WriteString(" -->\n\n")
	builder.WriteString("모델이 가상 환경에서 수행한 행동과 최종 상태 기반 판정을 담은 기록입니다.\n")
	return []byte(builder.String()), nil
}

func parseAgenticEvaluation(contents []byte) (AgenticEvaluation, error) {
	match := agenticEvaluationMarker.FindSubmatch(contents)
	if len(match) != 2 {
		return AgenticEvaluation{}, errors.New("에이전트 실험 정보가 없습니다")
	}
	var evaluation AgenticEvaluation
	if err := json.Unmarshal(match[1], &evaluation); err != nil {
		return AgenticEvaluation{}, errors.New("에이전트 실험 정보가 올바르지 않습니다")
	}
	evaluation = normalizeAgenticEvaluation(evaluation)
	if err := validateAgenticEvaluation(evaluation); err != nil {
		return AgenticEvaluation{}, err
	}
	return evaluation, nil
}

func agenticEvaluationSummary(evaluation AgenticEvaluation) AgenticEvaluationSummary {
	summary := AgenticEvaluationSummary{
		ID: evaluation.ID, ProfileName: evaluation.ProfileName, ProfileBaseURL: evaluation.ProfileBaseURL,
		Models: evaluation.ModelIDs, ScenarioCount: len(evaluation.ScenarioIDs), Repetitions: evaluation.Repetitions,
		Status: evaluation.Status, CreatedAt: evaluation.CreatedAt, UpdatedAt: evaluation.UpdatedAt, RunCount: len(evaluation.Runs),
	}
	for _, run := range evaluation.Runs {
		if run.Status != "pending" && run.Status != "running" {
			summary.FinishedRunCount++
		}
		if run.Result != nil && run.Result.Passed {
			summary.PassedRunCount++
		}
	}
	return summary
}
