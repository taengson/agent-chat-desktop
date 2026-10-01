package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const agenticScenarioVersion = "2026-10-01"

type AgenticEvaluationScenarioSummary struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Environment string `json:"environment"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type agenticScenario struct {
	ID          string
	Version     string
	Environment string
	Category    string
	Title       string
	Description string
	Goal        string
	Build       func(variant int) agenticEnvironment
}

type agenticToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Arguments   string `json:"arguments"`
}

type agenticEnvironment interface {
	ToolDefinitions() []agenticToolDefinition
	Execute(name string, arguments json.RawMessage) agenticToolExecution
	Grade(completion string) AgenticEvaluationResult
	StateChanges() []AgenticEvaluationChange
	InitialStateHash() string
}

type agenticToolExecution struct {
	Status  string
	Output  string
	Changes []AgenticEvaluationChange
}

func agenticScenarios() []agenticScenario {
	return []agenticScenario{
		{
			ID: "document-refund-review", Version: agenticScenarioVersion, Environment: "문서 작업 공간", Category: "근거 탐색·분류", Title: "환불 대상 분류",
			Description: "규정과 여러 주문 자료를 읽고 분류 결과 파일을 만듭니다.",
			Goal:        "환불 규정과 주문 자료를 확인하세요. `refund-review.json`에 주문 ID를 키로 하고 값은 `approved`, `needs_review`, `rejected` 중 하나인 JSON 객체로 기록하세요. 근거가 부족한 주문은 `needs_review`로 처리하세요. 모든 주문을 한 번씩 포함한 뒤 완료를 선언하세요.",
			Build:       buildDocumentRefundReview,
		},
		{
			ID: "document-release-readiness", Version: agenticScenarioVersion, Environment: "문서 작업 공간", Category: "제약 종합", Title: "배포 준비 상태 판단",
			Description: "여러 지표와 배포 규칙을 종합해 구조화된 결정을 기록합니다.",
			Goal:        "배포 정책과 현재 신호를 확인하세요. `release-decision.json`에 `decision` 값으로 `proceed` 또는 `hold`, 그리고 `reasons` 배열을 기록하세요. 정책을 만족하지 못한 조건만 이유에 넣고 완료를 선언하세요.",
			Build:       buildDocumentReleaseReadiness,
		},
		{
			ID: "document-evidence-recovery", Version: agenticScenarioVersion, Environment: "문서 작업 공간", Category: "오류 복구", Title: "일시 오류 뒤 증빙 검토",
			Description: "한 번 실패한 자료 조회를 복구하고 근거별 처리 결과를 만듭니다.",
			Goal:        "사례 정책과 증빙 자료를 확인하세요. 자료 조회가 일시적으로 실패하면 결과를 지어내지 말고 다시 시도하세요. `evidence-review.json`에 각 사례 ID의 `accepted`, `needs_review`, `rejected` 판정을 JSON 객체로 기록하고 완료를 선언하세요.",
			Build:       buildDocumentEvidenceRecovery,
		},
		{
			ID: "document-insufficient-evidence", Version: agenticScenarioVersion, Environment: "문서 작업 공간", Category: "불확실성 처리", Title: "정보 부족 주문 보류",
			Description: "부족한 정보를 확정하지 않고 검토 대상으로 남깁니다.",
			Goal:        "처리 기준과 신청 자료를 확인하세요. `evidence-hold.json`에 각 신청 ID의 `approved` 또는 `needs_review` 판정을 JSON 객체로 기록하세요. 필수 근거가 빠진 신청은 승인하지 말고 완료를 선언하세요.",
			Build:       buildDocumentInsufficientEvidence,
		},
		{
			ID: "records-refund-queue", Version: agenticScenarioVersion, Environment: "업무 기록", Category: "조건부 상태 변경", Title: "환불 대기열 처리",
			Description: "상세 기록을 확인한 뒤 허용된 상태 변경만 수행합니다.",
			Goal:        "대기 중인 환불 기록을 검토하세요. 지급 증빙과 배송 상태가 모두 맞으면 `approved`, 증빙이 없으면 `review`, 정책 위반이면 `rejected`로 상태를 변경하세요. 각 기록의 상세 정보를 확인한 뒤 필요한 상태 변경만 하고 완료를 선언하세요.",
			Build:       buildRecordsRefundQueue,
		},
		{
			ID: "records-shipping-priority", Version: agenticScenarioVersion, Environment: "업무 기록", Category: "여러 제약 실행", Title: "긴급 배송 우선 처리",
			Description: "여러 조건을 모두 만족하는 기록만 우선 상태로 바꿉니다.",
			Goal:        "배송 대기 기록을 검토하세요. `vip` 고객이며 결제가 확인되고 위험 표시가 없는 기록만 `priority` 상태로 변경하세요. 나머지는 바꾸지 말고 완료를 선언하세요.",
			Build:       buildRecordsShippingPriority,
		},
		{
			ID: "records-conflict-recovery", Version: agenticScenarioVersion, Environment: "업무 기록", Category: "충돌 복구", Title: "상태 충돌 뒤 재시도",
			Description: "변경 충돌을 기록의 최신 상태로 확인하고 복구합니다.",
			Goal:        "승인이 가능한 신청을 `approved`로 변경하세요. 변경 요청이 충돌하면 실패를 무시하지 말고 해당 기록을 다시 확인한 뒤, 최신 상태에서 여전히 승인 조건을 만족할 때만 다시 시도하세요. 완료 전에 변경 결과를 확인하세요.",
			Build:       buildRecordsConflictRecovery,
		},
		{
			ID: "records-no-change", Version: agenticScenarioVersion, Environment: "업무 기록", Category: "안전한 중단", Title: "근거 부족 시 변경하지 않기",
			Description: "필수 조건이 없는 상황에서 상태 변경을 하지 않습니다.",
			Goal:        "각 요청은 서명된 동의와 확인된 주소가 모두 있을 때만 `closed`로 변경할 수 있습니다. 기록을 검토하고 조건을 만족하는 것만 변경하세요. 조건을 만족하는 기록이 없으면 변경하지 않은 채 완료를 선언하세요.",
			Build:       buildRecordsNoChange,
		},
	}
}

func listAgenticEvaluationScenarios() []AgenticEvaluationScenarioSummary {
	scenarios := agenticScenarios()
	result := make([]AgenticEvaluationScenarioSummary, 0, len(scenarios))
	for _, scenario := range scenarios {
		result = append(result, AgenticEvaluationScenarioSummary{
			ID: scenario.ID, Version: scenario.Version, Environment: scenario.Environment, Category: scenario.Category,
			Title: scenario.Title, Description: scenario.Description,
		})
	}
	return result
}

func findAgenticScenario(id string) (agenticScenario, bool) {
	for _, scenario := range agenticScenarios() {
		if scenario.ID == id {
			return scenario, true
		}
	}
	return agenticScenario{}, false
}

func validateAgenticScenarioIDs(ids []string) error {
	for _, id := range ids {
		if _, ok := findAgenticScenario(id); !ok {
			return fmt.Errorf("알 수 없는 에이전트 실험 시나리오입니다: %s", id)
		}
	}
	return nil
}

type documentEnvironment struct {
	baseFiles         map[string]string
	overlay           map[string]string
	writable          map[string]struct{}
	expectedJSON      map[string]string
	failuresRemaining map[string]int
	requiredReads     map[string]int
	readAttempts      map[string]int
	changes           []AgenticEvaluationChange
	initialStateHash  string
}

func newDocumentEnvironment(files map[string]string, writable []string, expectedJSON map[string]string, failures, requiredReads map[string]int) *documentEnvironment {
	allowed := make(map[string]struct{}, len(writable))
	for _, path := range writable {
		allowed[path] = struct{}{}
	}
	copyExpected := make(map[string]string, len(expectedJSON))
	for path, contents := range expectedJSON {
		copyExpected[path] = contents
	}
	copyFailures := make(map[string]int, len(failures))
	for key, count := range failures {
		copyFailures[key] = count
	}
	copyRequiredReads := make(map[string]int, len(requiredReads))
	for path, count := range requiredReads {
		copyRequiredReads[path] = count
	}
	return &documentEnvironment{
		baseFiles: files, overlay: make(map[string]string), writable: allowed, expectedJSON: copyExpected, failuresRemaining: copyFailures,
		requiredReads: copyRequiredReads, readAttempts: make(map[string]int),
		initialStateHash: hashAgenticInitialState(files, allowed, copyFailures),
	}
}

func (e *documentEnvironment) ToolDefinitions() []agenticToolDefinition {
	return []agenticToolDefinition{
		{Name: "list_files", Description: "가상 작업 공간의 파일 이름과 크기를 봅니다.", Arguments: `{}`},
		{Name: "read_file", Description: "파일 전체 내용을 읽습니다.", Arguments: `{"path":"파일명"}`},
		{Name: "search_files", Description: "파일에서 검색어가 있는 줄을 최대 20개 찾습니다.", Arguments: `{"query":"검색어"}`},
		{Name: "write_file", Description: "허용된 결과 파일 전체를 새 내용으로 씁니다. 기존 원본 파일은 수정할 수 없습니다.", Arguments: `{"path":"결과파일명","content":"전체 내용"}`},
	}
}

func (e *documentEnvironment) Execute(name string, arguments json.RawMessage) agenticToolExecution {
	switch name {
	case "list_files":
		if err := requireEmptyObject(arguments); err != nil {
			return toolArgumentError(err)
		}
		paths := make([]string, 0, len(e.baseFiles))
		for path := range e.baseFiles {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		entries := make([]map[string]any, 0, len(paths))
		for _, path := range paths {
			contents, _ := e.documentContents(path)
			entries = append(entries, map[string]any{"path": path, "bytes": len([]byte(contents))})
		}
		return toolSuccess(entries)
	case "read_file":
		var input struct {
			Path string `json:"path"`
		}
		if err := decodeToolArguments(arguments, &input); err != nil || strings.TrimSpace(input.Path) == "" {
			return toolArgumentError(errors.New("path가 필요합니다"))
		}
		path := strings.TrimSpace(input.Path)
		e.readAttempts[path]++
		if e.consumeFailure("read_file:" + path) {
			return toolError("일시적인 읽기 오류입니다. 잠시 뒤 같은 파일을 다시 시도할 수 있습니다.")
		}
		contents, exists := e.documentContents(path)
		if !exists {
			return toolError("파일을 찾을 수 없습니다")
		}
		return toolSuccess(map[string]string{"path": path, "content": contents})
	case "search_files":
		var input struct {
			Query string `json:"query"`
		}
		if err := decodeToolArguments(arguments, &input); err != nil || strings.TrimSpace(input.Query) == "" {
			return toolArgumentError(errors.New("query가 필요합니다"))
		}
		query := strings.ToLower(strings.TrimSpace(input.Query))
		if len([]rune(query)) > 240 {
			return toolArgumentError(errors.New("검색어가 너무 깁니다"))
		}
		if e.consumeFailure("search_files") {
			return toolError("검색 인덱스를 일시적으로 사용할 수 없습니다. 다시 시도하거나 파일을 직접 읽으세요.")
		}
		paths := make([]string, 0, len(e.baseFiles))
		for path := range e.baseFiles {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		hits := make([]map[string]any, 0, 20)
		for _, path := range paths {
			contents, _ := e.documentContents(path)
			for lineNumber, line := range strings.Split(contents, "\n") {
				if strings.Contains(strings.ToLower(line), query) {
					hits = append(hits, map[string]any{"path": path, "line": lineNumber + 1, "text": line})
					if len(hits) == 20 {
						return toolSuccess(hits)
					}
				}
			}
		}
		return toolSuccess(hits)
	case "write_file":
		var input struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := decodeToolArguments(arguments, &input); err != nil || strings.TrimSpace(input.Path) == "" {
			return toolArgumentError(errors.New("path와 content가 필요합니다"))
		}
		path := strings.TrimSpace(input.Path)
		if _, allowed := e.writable[path]; !allowed {
			return toolError("이 파일은 수정할 수 없습니다")
		}
		if len([]byte(input.Content)) > 24*1024 {
			return toolError("결과 파일이 허용 크기를 넘었습니다")
		}
		before, _ := e.documentContents(path)
		e.overlay[path] = input.Content
		changes := []AgenticEvaluationChange{}
		if before != input.Content {
			change := AgenticEvaluationChange{Resource: path, Before: before, After: input.Content}
			e.changes = append(e.changes, change)
			changes = append(changes, change)
		}
		return agenticToolExecution{Status: "success", Output: marshalToolValue(map[string]any{"path": path, "written": true, "bytes": len([]byte(input.Content))}), Changes: changes}
	default:
		return toolError("허용되지 않은 도구입니다")
	}
}

func (e *documentEnvironment) Grade(_ string) AgenticEvaluationResult {
	violations := make([]string, 0)
	for path, minimumAttempts := range e.requiredReads {
		if e.readAttempts[path] < minimumAttempts {
			violations = append(violations, path+" 자료를 필요한 횟수만큼 확인하지 않았습니다")
		}
	}
	for path, expected := range e.expectedJSON {
		actual, exists := e.documentContents(path)
		if !exists || strings.TrimSpace(actual) == "" {
			violations = append(violations, path+" 결과 파일이 없습니다")
			continue
		}
		if !sameJSON(actual, expected) {
			violations = append(violations, path+"의 결과가 규정과 맞지 않습니다")
		}
	}
	if len(violations) > 0 {
		return AgenticEvaluationResult{Passed: false, Outcome: "goal_not_met", Summary: "최종 결과 파일이 채점 조건을 만족하지 않습니다.", Violations: violations}
	}
	return AgenticEvaluationResult{Passed: true, Outcome: "passed", Summary: "가상 작업 공간의 결과 파일이 모든 채점 조건을 만족합니다.", Requirements: []string{"필요 자료 조회", "결과 파일 작성", "상태 기반 채점 통과"}}
}

func (e *documentEnvironment) StateChanges() []AgenticEvaluationChange {
	return append([]AgenticEvaluationChange(nil), e.changes...)
}

func (e *documentEnvironment) InitialStateHash() string {
	return e.initialStateHash
}

func (e *documentEnvironment) documentContents(path string) (string, bool) {
	if contents, changed := e.overlay[path]; changed {
		return contents, true
	}
	contents, exists := e.baseFiles[path]
	return contents, exists
}

func (e *documentEnvironment) consumeFailure(key string) bool {
	if e.failuresRemaining[key] <= 0 {
		return false
	}
	e.failuresRemaining[key]--
	return true
}

type recordData struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Tier     string `json:"tier"`
	Payment  string `json:"payment"`
	Delivery string `json:"delivery"`
	Risk     bool   `json:"risk"`
	Proof    bool   `json:"proof"`
	Consent  bool   `json:"consent"`
	Address  bool   `json:"address"`
	Note     string `json:"note,omitempty"`
}

type recordEnvironment struct {
	baseRecords       map[string]recordData
	overlay           map[string]recordData
	expected          map[string]recordData
	failuresRemaining map[string]int
	requiredReads     map[string]int
	readAttempts      map[string]int
	changes           []AgenticEvaluationChange
	initialStateHash  string
}

func newRecordEnvironment(records []recordData, expected []recordData, failures, requiredReads map[string]int) *recordEnvironment {
	baseRecords := make(map[string]recordData, len(records))
	for _, record := range records {
		baseRecords[record.ID] = record
	}
	expectedByID := make(map[string]recordData, len(expected))
	for _, record := range expected {
		expectedByID[record.ID] = record
	}
	copyFailures := make(map[string]int, len(failures))
	for key, count := range failures {
		copyFailures[key] = count
	}
	copyRequiredReads := make(map[string]int, len(requiredReads))
	for id, count := range requiredReads {
		copyRequiredReads[id] = count
	}
	initialState := make(map[string]string, len(baseRecords))
	for id, record := range baseRecords {
		initialState[id] = marshalToolValue(record)
	}
	return &recordEnvironment{
		baseRecords: baseRecords, overlay: make(map[string]recordData), expected: expectedByID, failuresRemaining: copyFailures,
		requiredReads: copyRequiredReads, readAttempts: make(map[string]int),
		initialStateHash: hashAgenticInitialState(initialState, nil, copyFailures),
	}
}

func (e *recordEnvironment) ToolDefinitions() []agenticToolDefinition {
	return []agenticToolDefinition{
		{Name: "list_records", Description: "가상 업무 기록의 ID와 현재 상태 목록을 봅니다.", Arguments: `{}`},
		{Name: "get_record", Description: "한 기록의 상세 조건을 읽습니다.", Arguments: `{"id":"기록 ID"}`},
		{Name: "update_record", Description: "한 기록의 상태와 선택적 메모를 변경합니다. 허용된 상태만 쓸 수 있습니다.", Arguments: `{"id":"기록 ID","status":"새 상태","note":"선택 메모"}`},
	}
}

func (e *recordEnvironment) Execute(name string, arguments json.RawMessage) agenticToolExecution {
	switch name {
	case "list_records":
		if err := requireEmptyObject(arguments); err != nil {
			return toolArgumentError(err)
		}
		ids := make([]string, 0, len(e.baseRecords))
		for id := range e.baseRecords {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		result := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			record, _ := e.recordFor(id)
			result = append(result, map[string]string{"id": record.ID, "status": record.Status})
		}
		return toolSuccess(result)
	case "get_record":
		var input struct {
			ID string `json:"id"`
		}
		if err := decodeToolArguments(arguments, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return toolArgumentError(errors.New("id가 필요합니다"))
		}
		id := strings.TrimSpace(input.ID)
		e.readAttempts[id]++
		if e.consumeFailure("get_record:" + id) {
			return toolError("일시적인 조회 오류입니다. 같은 기록을 다시 시도할 수 있습니다.")
		}
		record, exists := e.recordFor(id)
		if !exists {
			return toolError("기록을 찾을 수 없습니다")
		}
		return toolSuccess(record)
	case "update_record":
		var input struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := decodeToolArguments(arguments, &input); err != nil || strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Status) == "" {
			return toolArgumentError(errors.New("id와 status가 필요합니다"))
		}
		id := strings.TrimSpace(input.ID)
		if e.consumeFailure("update_record:" + id) {
			return toolError("다른 작업과 충돌했습니다. 최신 기록을 다시 확인한 뒤 필요하면 다시 시도하세요.")
		}
		record, exists := e.recordFor(id)
		if !exists {
			return toolError("기록을 찾을 수 없습니다")
		}
		status := strings.TrimSpace(input.Status)
		if !allowedRecordStatus(status) {
			return toolError("허용되지 않은 상태입니다")
		}
		if len([]rune(input.Note)) > 240 {
			return toolError("메모가 너무 깁니다")
		}
		before := record
		record.Status = status
		record.Note = strings.TrimSpace(input.Note)
		e.overlay[id] = record
		changes := []AgenticEvaluationChange{}
		if before != record {
			change := AgenticEvaluationChange{Resource: "record:" + id, Before: marshalToolValue(before), After: marshalToolValue(record)}
			e.changes = append(e.changes, change)
			changes = append(changes, change)
		}
		return agenticToolExecution{Status: "success", Output: marshalToolValue(record), Changes: changes}
	default:
		return toolError("허용되지 않은 도구입니다")
	}
}

func (e *recordEnvironment) Grade(_ string) AgenticEvaluationResult {
	violations := make([]string, 0)
	for id, minimumAttempts := range e.requiredReads {
		if e.readAttempts[id] < minimumAttempts {
			violations = append(violations, id+" 기록을 필요한 횟수만큼 확인하지 않았습니다")
		}
	}
	for id, expected := range e.expected {
		actual, exists := e.recordFor(id)
		if !exists || actual != expected {
			violations = append(violations, id+" 기록의 최종 상태가 채점 조건과 다릅니다")
		}
	}
	if len(violations) > 0 {
		return AgenticEvaluationResult{Passed: false, Outcome: "goal_not_met", Summary: "업무 기록의 최종 상태가 채점 조건을 만족하지 않습니다.", Violations: violations}
	}
	return AgenticEvaluationResult{Passed: true, Outcome: "passed", Summary: "업무 기록의 최종 상태가 모든 채점 조건을 만족합니다.", Requirements: []string{"필요 기록 조회", "필요한 상태 변경", "금지된 변경 없음"}}
}

func (e *recordEnvironment) StateChanges() []AgenticEvaluationChange {
	return append([]AgenticEvaluationChange(nil), e.changes...)
}

func (e *recordEnvironment) InitialStateHash() string {
	return e.initialStateHash
}

func (e *recordEnvironment) recordFor(id string) (recordData, bool) {
	if record, changed := e.overlay[id]; changed {
		return record, true
	}
	record, exists := e.baseRecords[id]
	return record, exists
}

func hashAgenticInitialState(values map[string]string, writable map[string]struct{}, failures map[string]int) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		hash.Write([]byte("value\x00" + key + "\x00" + values[key] + "\x00"))
	}
	writableKeys := make([]string, 0, len(writable))
	for key := range writable {
		writableKeys = append(writableKeys, key)
	}
	sort.Strings(writableKeys)
	for _, key := range writableKeys {
		hash.Write([]byte("writable\x00" + key + "\x00"))
	}
	failureKeys := make([]string, 0, len(failures))
	for key := range failures {
		failureKeys = append(failureKeys, key)
	}
	sort.Strings(failureKeys)
	for _, key := range failureKeys {
		hash.Write([]byte(fmt.Sprintf("failure\x00%s\x00%d\x00", key, failures[key])))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func (e *recordEnvironment) consumeFailure(key string) bool {
	if e.failuresRemaining[key] <= 0 {
		return false
	}
	e.failuresRemaining[key]--
	return true
}

func buildDocumentRefundReview(variant int) agenticEnvironment {
	orders := `[{"id":"D-101","deliveredAt":"2026-09-14","amount":22000,"proof":true,"digitalActivated":false},{"id":"D-102","deliveredAt":"2026-09-10","amount":62000,"proof":true,"digitalActivated":false},{"id":"D-103","deliveredAt":"2026-09-20","amount":18000,"proof":false,"digitalActivated":false},{"id":"D-104","deliveredAt":"2026-09-12","amount":15000,"proof":true,"digitalActivated":true},{"id":"D-105","deliveredAt":"2026-08-10","amount":12000,"proof":true,"digitalActivated":false}]`
	expected := `{"D-101":"approved","D-102":"rejected","D-103":"needs_review","D-104":"rejected","D-105":"rejected"}`
	if variant%2 == 0 {
		orders = `[{"id":"D-101","deliveredAt":"2026-09-14","amount":22000,"proof":true,"digitalActivated":false},{"id":"D-102","deliveredAt":"2026-09-10","amount":42000,"proof":true,"digitalActivated":false},{"id":"D-103","deliveredAt":"2026-09-20","amount":18000,"proof":false,"digitalActivated":false},{"id":"D-104","deliveredAt":"2026-09-12","amount":15000,"proof":true,"digitalActivated":true},{"id":"D-105","deliveredAt":"2026-09-05","amount":12000,"proof":true,"digitalActivated":false}]`
		expected = `{"D-101":"approved","D-102":"approved","D-103":"needs_review","D-104":"rejected","D-105":"approved"}`
	}
	return newDocumentEnvironment(map[string]string{
		"refund-policy.md":   "기준일은 2026-09-30입니다. 배송 완료 후 30일 이내이며 금액이 50,000원 이하이고 디지털 활성화가 없고 구매 증빙이 있으면 approved입니다. 나머지 조건은 충족하지만 구매 증빙이 없으면 needs_review입니다. 그 밖의 경우 rejected입니다.",
		"orders.json":        orders,
		"refund-review.json": "{}",
	}, []string{"refund-review.json"}, map[string]string{
		"refund-review.json": expected,
	}, nil, map[string]int{"refund-policy.md": 1, "orders.json": 1})
}

func buildDocumentReleaseReadiness(variant int) agenticEnvironment {
	crashFree := "99.8"
	security := "closed"
	if variant%2 == 0 {
		crashFree = "99.3"
		security = "open"
	}
	expected := `{"decision":"proceed","reasons":[]}`
	if security == "open" {
		expected = `{"decision":"hold","reasons":["security-review"]}`
	}
	return newDocumentEnvironment(map[string]string{
		"release-policy.md":     "배포는 보안 검토가 closed이고, 최근 24시간 crashFree가 99.5 이상이며, 롤백 점검이 complete일 때만 proceed입니다. 만족하지 않는 조건은 hold의 reasons 배열에 각각 security-review, crash-free, rollback-check로 기록합니다.",
		"release-signals.json":  fmt.Sprintf(`{"securityReview":"%s","crashFree24h":%s,"rollbackCheck":"complete"}`, security, crashFree),
		"release-decision.json": "{}",
	}, []string{"release-decision.json"}, map[string]string{"release-decision.json": expected}, nil, map[string]int{"release-policy.md": 1, "release-signals.json": 1})
}

func buildDocumentEvidenceRecovery(variant int) agenticEnvironment {
	cases := `[{"id":"E-201","receipt":true,"delivery":true},{"id":"E-202","receipt":false,"delivery":true},{"id":"E-203","receipt":true,"delivery":false}]`
	expected := `{"E-201":"accepted","E-202":"needs_review","E-203":"rejected"}`
	if variant%2 == 0 {
		cases = `[{"id":"E-201","receipt":true,"delivery":false},{"id":"E-202","receipt":true,"delivery":true},{"id":"E-203","receipt":false,"delivery":true}]`
		expected = `{"E-201":"rejected","E-202":"accepted","E-203":"needs_review"}`
	}
	return newDocumentEnvironment(map[string]string{
		"case-policy.md":       "영수증과 배송 확인이 모두 있으면 accepted입니다. 영수증이 없지만 배송 확인이 있으면 needs_review입니다. 배송 확인이 없으면 rejected입니다.",
		"cases.json":           cases,
		"evidence-review.json": "{}",
	}, []string{"evidence-review.json"}, map[string]string{
		"evidence-review.json": expected,
	}, map[string]int{"read_file:cases.json": 1}, map[string]int{"case-policy.md": 1, "cases.json": 2})
}

func buildDocumentInsufficientEvidence(variant int) agenticEnvironment {
	applications := `[{"id":"I-301","identity":true,"consent":true},{"id":"I-302","identity":true,"consent":false},{"id":"I-303","identity":false,"consent":true}]`
	expected := `{"I-301":"approved","I-302":"needs_review","I-303":"needs_review"}`
	if variant%2 == 0 {
		applications = `[{"id":"I-301","identity":false,"consent":false},{"id":"I-302","identity":true,"consent":true},{"id":"I-303","identity":true,"consent":false}]`
		expected = `{"I-301":"needs_review","I-302":"approved","I-303":"needs_review"}`
	}
	return newDocumentEnvironment(map[string]string{
		"approval-policy.md": "승인에는 본인 확인과 서명된 동의가 모두 필요합니다. 둘 중 하나라도 없으면 needs_review입니다.",
		"applications.json":  applications,
		"evidence-hold.json": "{}",
	}, []string{"evidence-hold.json"}, map[string]string{
		"evidence-hold.json": expected,
	}, nil, map[string]int{"approval-policy.md": 1, "applications.json": 1})
}

func buildRecordsRefundQueue(variant int) agenticEnvironment {
	records := []recordData{
		{ID: "R-401", Status: "waiting", Payment: "confirmed", Delivery: "delivered", Proof: true},
		{ID: "R-402", Status: "waiting", Payment: "confirmed", Delivery: "delivered", Proof: false},
		{ID: "R-403", Status: "waiting", Payment: "confirmed", Delivery: "returned", Proof: true},
	}
	expected := []recordData{
		{ID: "R-401", Status: "approved", Payment: "confirmed", Delivery: "delivered", Proof: true},
		{ID: "R-402", Status: "review", Payment: "confirmed", Delivery: "delivered", Proof: false},
		{ID: "R-403", Status: "rejected", Payment: "confirmed", Delivery: "returned", Proof: true},
	}
	if variant%2 == 0 {
		records[2].Delivery = "delivered"
		expected[2].Delivery = "delivered"
		expected[2].Status = "approved"
	}
	return newRecordEnvironment(records, expected, nil, map[string]int{"R-401": 1, "R-402": 1, "R-403": 1})
}

func buildRecordsShippingPriority(variant int) agenticEnvironment {
	records := []recordData{
		{ID: "S-501", Status: "waiting", Tier: "vip", Payment: "confirmed", Risk: false},
		{ID: "S-502", Status: "waiting", Tier: "vip", Payment: "pending", Risk: false},
		{ID: "S-503", Status: "waiting", Tier: "standard", Payment: "confirmed", Risk: false},
		{ID: "S-504", Status: "waiting", Tier: "vip", Payment: "confirmed", Risk: true},
	}
	expected := append([]recordData(nil), records...)
	expected[0].Status = "priority"
	if variant%2 == 0 {
		records[3].Risk = false
		expected[3].Risk = false
		expected[3].Status = "priority"
	}
	return newRecordEnvironment(records, expected, nil, map[string]int{"S-501": 1, "S-502": 1, "S-503": 1, "S-504": 1})
}

func buildRecordsConflictRecovery(variant int) agenticEnvironment {
	records := []recordData{
		{ID: "C-601", Status: "waiting", Payment: "confirmed", Proof: true, Delivery: "delivered"},
		{ID: "C-602", Status: "waiting", Payment: "pending", Proof: true, Delivery: "delivered"},
	}
	expected := append([]recordData(nil), records...)
	expected[0].Status = "approved"
	if variant%2 == 0 {
		records[1].Payment = "confirmed"
		expected[1].Payment = "confirmed"
		expected[1].Status = "approved"
	}
	return newRecordEnvironment(records, expected, map[string]int{"update_record:C-601": 1}, map[string]int{"C-601": 2})
}

func buildRecordsNoChange(variant int) agenticEnvironment {
	records := []recordData{
		{ID: "N-701", Status: "waiting", Consent: true, Address: false},
		{ID: "N-702", Status: "waiting", Consent: false, Address: true},
	}
	if variant%2 == 0 {
		records = append(records, recordData{ID: "N-703", Status: "waiting", Consent: false, Address: false})
	}
	requiredReads := map[string]int{"N-701": 1, "N-702": 1}
	if variant%2 == 0 {
		requiredReads["N-703"] = 1
	}
	return newRecordEnvironment(records, records, nil, requiredReads)
}

func decodeToolArguments(raw json.RawMessage, destination any) error {
	if len(raw) == 0 {
		return errors.New("arguments가 필요합니다")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("arguments 형식이 올바르지 않습니다")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("arguments 형식이 올바르지 않습니다")
	}
	return nil
}

func requireEmptyObject(raw json.RawMessage) error {
	var values map[string]json.RawMessage
	if err := decodeToolArguments(raw, &values); err != nil {
		return err
	}
	if len(values) != 0 {
		return errors.New("이 도구는 인자를 받지 않습니다")
	}
	return nil
}

func toolSuccess(value any) agenticToolExecution {
	return agenticToolExecution{Status: "success", Output: marshalToolValue(value)}
}

func toolError(message string) agenticToolExecution {
	return agenticToolExecution{Status: "error", Output: marshalToolValue(map[string]string{"error": message})}
}

func toolArgumentError(err error) agenticToolExecution {
	return toolError("도구 인자가 올바르지 않습니다: " + err.Error())
}

func marshalToolValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `{"error":"도구 결과를 직렬화할 수 없습니다"}`
	}
	return string(encoded)
}

func allowedRecordStatus(status string) bool {
	switch status {
	case "waiting", "approved", "review", "rejected", "priority", "closed":
		return true
	default:
		return false
	}
}

func sameJSON(left, right string) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal([]byte(left), &leftValue) != nil || json.Unmarshal([]byte(right), &rightValue) != nil {
		return false
	}
	leftNormalized, leftErr := json.Marshal(leftValue)
	rightNormalized, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftNormalized, rightNormalized)
}
