package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDocumentEnvironmentRetriesWithoutLeakingState(t *testing.T) {
	environment := buildDocumentEvidenceRecovery(1)
	policy := environment.Execute("read_file", json.RawMessage(`{"path":"case-policy.md"}`))
	if policy.Status != "success" {
		t.Fatalf("policy read = %#v", policy)
	}
	first := environment.Execute("read_file", json.RawMessage(`{"path":"cases.json"}`))
	if first.Status != "error" {
		t.Fatalf("first read status = %q, want error", first.Status)
	}
	second := environment.Execute("read_file", json.RawMessage(`{"path":"cases.json"}`))
	if second.Status != "success" || !strings.Contains(second.Output, "E-201") {
		t.Fatalf("second read = %#v", second)
	}
	written := environment.Execute("write_file", json.RawMessage(`{"path":"evidence-review.json","content":"{\"E-201\":\"accepted\",\"E-202\":\"needs_review\",\"E-203\":\"rejected\"}"}`))
	if written.Status != "success" || len(written.Changes) != 1 {
		t.Fatalf("write = %#v", written)
	}
	document := environment.(*documentEnvironment)
	if document.baseFiles["evidence-review.json"] != "{}" || document.overlay["evidence-review.json"] == "{}" {
		t.Fatalf("document overlay did not preserve base state: %#v", document)
	}
	if result := environment.Grade("done"); !result.Passed {
		t.Fatalf("Grade() = %#v", result)
	}

	newEnvironment := buildDocumentEvidenceRecovery(1)
	if result := newEnvironment.Grade("done"); result.Passed {
		t.Fatalf("fresh environment unexpectedly retained changes: %#v", result)
	}
}

func TestStateGradeRequiresEvidenceDiscovery(t *testing.T) {
	environment := buildDocumentRefundReview(1)
	written := environment.Execute("write_file", json.RawMessage(`{"path":"refund-review.json","content":"{\"D-101\":\"approved\",\"D-102\":\"rejected\",\"D-103\":\"needs_review\",\"D-104\":\"rejected\",\"D-105\":\"rejected\"}"}`))
	if written.Status != "success" {
		t.Fatalf("write = %#v", written)
	}
	result := environment.Grade("done")
	if result.Passed || len(result.Violations) == 0 {
		t.Fatalf("Grade() = %#v, want discovery violation", result)
	}
}

func TestDocumentEnvironmentRejectsForbiddenToolAndPath(t *testing.T) {
	environment := buildDocumentRefundReview(1)
	if execution := environment.Execute("shell", json.RawMessage(`{"command":"rm -rf /"}`)); execution.Status != "error" {
		t.Fatalf("forbidden tool status = %q, want error", execution.Status)
	}
	if execution := environment.Execute("write_file", json.RawMessage(`{"path":"../refund-review.json","content":"{}"}`)); execution.Status != "error" {
		t.Fatalf("forbidden path status = %q, want error", execution.Status)
	}
	if execution := environment.Execute("write_file", json.RawMessage(`{"path":"refund-policy.md","content":"changed"}`)); execution.Status != "error" {
		t.Fatalf("read-only path status = %q, want error", execution.Status)
	}
}

func TestRecordEnvironmentRejectsUnnecessaryChange(t *testing.T) {
	environment := buildRecordsNoChange(1)
	change := environment.Execute("update_record", json.RawMessage(`{"id":"N-701","status":"closed"}`))
	if change.Status != "success" {
		t.Fatalf("update_record() = %#v", change)
	}
	result := environment.Grade("done")
	if result.Passed || len(result.Violations) == 0 {
		t.Fatalf("Grade() = %#v, want failed with violation", result)
	}
}

func TestParseAgenticModelActionRequiresOneStrictJSONObject(t *testing.T) {
	action, err := parseAgenticModelAction(`{"type":"tool","name":"read_file","arguments":{"path":"policy.md"}}`)
	if err != nil || action.Type != "tool" || action.Name != "read_file" {
		t.Fatalf("parseAgenticModelAction() = %#v, %v", action, err)
	}
	if _, err := parseAgenticModelAction("```json\n{}\n```"); err == nil {
		t.Fatal("parseAgenticModelAction() accepted markdown")
	}
	if _, err := parseAgenticModelAction(`{"type":"tool","name":"read_file","arguments":[]} `); err == nil {
		t.Fatal("parseAgenticModelAction() accepted a non-object arguments value")
	}
	if _, err := parseAgenticModelAction(`{"type":"tool","name":"read_file","arguments":null}`); err == nil {
		t.Fatal("parseAgenticModelAction() accepted null arguments")
	}
	if _, err := parseAgenticModelAction(`{"type":"complete","summary":"done"} {"type":"complete","summary":"again"}`); err == nil {
		t.Fatal("parseAgenticModelAction() accepted trailing JSON")
	}
}

func TestAgenticEvaluationRunsShareVariantInitialStateAcrossModels(t *testing.T) {
	runs := makeAgenticEvaluationRuns([]string{"model-a", "model-b"}, []string{"document-release-readiness"}, 2)
	if len(runs) != 4 {
		t.Fatalf("run count = %d, want 4", len(runs))
	}
	if runs[0].Variant != 1 || runs[1].Variant != 1 || runs[2].Variant != 2 || runs[3].Variant != 2 {
		t.Fatalf("variants = %#v", runs)
	}
	if runs[0].InitialStateHash == "" || runs[0].InitialStateHash != runs[1].InitialStateHash {
		t.Fatalf("same-variant initial state = %q, %q", runs[0].InitialStateHash, runs[1].InitialStateHash)
	}
	if runs[0].InitialStateHash == runs[2].InitialStateHash {
		t.Fatalf("different variants unexpectedly share initial state hash: %q", runs[0].InitialStateHash)
	}
	if runs[0].GraderVersion != agenticGraderVersion {
		t.Fatalf("grader version = %q, want %q", runs[0].GraderVersion, agenticGraderVersion)
	}
}

func TestEveryScenarioHasDeterministicDistinctVariants(t *testing.T) {
	for _, scenario := range agenticScenarios() {
		first := scenario.Build(1).InitialStateHash()
		second := scenario.Build(2).InitialStateHash()
		if first == "" || second == "" {
			t.Fatalf("%s has an empty initial-state hash", scenario.ID)
		}
		if first == second {
			t.Fatalf("%s does not change initial state between variants", scenario.ID)
		}
		if first != scenario.Build(1).InitialStateHash() {
			t.Fatalf("%s variant 1 is not deterministic", scenario.ID)
		}
	}
}

func TestAgenticQueueAllowsOnlyOneEvaluation(t *testing.T) {
	app := NewApp()
	firstCancel := func() {}
	if err := app.reserveAgenticEvaluation("agentic-one", firstCancel); err != nil {
		t.Fatalf("reserve first evaluation: %v", err)
	}
	if err := app.reserveAgenticEvaluation("agentic-two", func() {}); err == nil {
		t.Fatal("second evaluation was accepted while the first was active")
	}
	app.releaseAgenticEvaluation("agentic-one")
	if err := app.reserveAgenticEvaluation("agentic-two", func() {}); err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	app.releaseAgenticEvaluation("agentic-two")
}

func TestAgenticRunStatusClassifiesCancellationAndTimeout(t *testing.T) {
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if status := agenticRunStatusForContext(cancelledContext); status != "cancelled" {
		t.Fatalf("cancelled status = %q", status)
	}
	timedOutContext, timedOutCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer timedOutCancel()
	time.Sleep(time.Millisecond)
	if status := agenticRunStatusForContext(timedOutContext); status != "time_limit" {
		t.Fatalf("timeout status = %q", status)
	}
}

func TestAgenticEvaluationStorePersistsAndMarksInterruptedRuns(t *testing.T) {
	store := newAgenticEvaluationStore(t.TempDir())
	scenario, ok := findAgenticScenario("records-no-change")
	if !ok {
		t.Fatal("records-no-change scenario is missing")
	}
	evaluation := AgenticEvaluation{
		ID: "evaluation-1", ProfileID: "profile-1", ProfileName: "테스트", ProfileBaseURL: "http://localhost:8000",
		ModelIDs: []string{"model-a"}, ScenarioIDs: []string{scenario.ID}, Repetitions: 1, Status: "running",
		Runs: []AgenticEvaluationRun{{
			ID: "run-1", Model: "model-a", ScenarioID: scenario.ID, ScenarioVersion: scenario.Version,
			Environment: scenario.Environment, Category: scenario.Category, Title: scenario.Title, Goal: scenario.Goal,
			Variant: 1, Status: "running", Actions: []AgenticEvaluationAction{},
		}},
	}
	created, err := store.Create(evaluation)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.MarkInterrupted(); err != nil {
		t.Fatalf("MarkInterrupted() error = %v", err)
	}
	opened, err := store.Open(created.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if opened.Status != "cancelled" || opened.Runs[0].Status != "cancelled" || opened.Runs[0].Result == nil || opened.Runs[0].Result.Outcome != "interrupted" {
		t.Fatalf("interrupted record = %#v", opened)
	}
}

func TestAgenticEvaluationStoreBoundsCompletedHistory(t *testing.T) {
	store := newAgenticEvaluationStore(t.TempDir())
	scenario, ok := findAgenticScenario("records-no-change")
	if !ok {
		t.Fatal("records-no-change scenario is missing")
	}
	for index := 0; index <= maxStoredAgenticEvaluations; index++ {
		initialHash := scenario.Build(1).InitialStateHash()
		evaluation := AgenticEvaluation{
			ID: newConversationID(), ProfileID: "profile-1", ProfileName: "테스트", ProfileBaseURL: "http://localhost:8000",
			ModelIDs: []string{"model-a"}, ScenarioIDs: []string{scenario.ID}, Repetitions: 1,
			ExecutionRules: defaultAgenticExecutionRules(), Status: "completed",
			Runs: []AgenticEvaluationRun{{
				ID: newConversationID(), Model: "model-a", ScenarioID: scenario.ID, ScenarioVersion: scenario.Version,
				InitialStateHash: initialHash, GraderVersion: agenticGraderVersion,
				Environment: scenario.Environment, Category: scenario.Category, Title: scenario.Title, Goal: scenario.Goal,
				Variant: 1, Status: "success", Actions: []AgenticEvaluationAction{},
			}},
		}
		if _, err := store.Create(evaluation); err != nil {
			t.Fatalf("Create(%d) error = %v", index, err)
		}
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != maxStoredAgenticEvaluations {
		t.Fatalf("stored summary count = %d, want %d", len(summaries), maxStoredAgenticEvaluations)
	}
}

func TestAgenticEvaluationRunsToolsUntilStateBasedSuccess(t *testing.T) {
	responses := []string{
		`{"type":"tool","name":"list_records","arguments":{}}`,
		`{"type":"tool","name":"get_record","arguments":{"id":"C-601"}}`,
		`{"type":"tool","name":"update_record","arguments":{"id":"C-601","status":"approved"}}`,
		`{"type":"tool","name":"get_record","arguments":{"id":"C-601"}}`,
		`{"type":"tool","name":"update_record","arguments":{"id":"C-601","status":"approved"}}`,
		`{"type":"tool","name":"get_record","arguments":{"id":"C-601"}}`,
		`{"type":"complete","summary":"승인 상태를 확인했습니다."}`,
	}
	var mu sync.Mutex
	responseIndex := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", request.URL.Path)
		}
		mu.Lock()
		if responseIndex >= len(responses) {
			mu.Unlock()
			t.Fatalf("received more requests than expected")
		}
		response := responses[responseIndex]
		responseIndex++
		mu.Unlock()
		encoded, _ := json.Marshal(response)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", encoded)
		fmt.Fprint(writer, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	app := NewApp()
	app.agenticEvaluations = newAgenticEvaluationStore(t.TempDir())
	finished := make(chan AgenticEvaluationEvent, 1)
	app.agenticEventSink = func(event AgenticEvaluationEvent) {
		if event.Type == "finished" {
			finished <- event
		}
	}
	created, err := app.StartAgenticEvaluation(AgenticEvaluationStartRequest{
		Profile:     ConnectionProfile{BaseURL: server.URL, APIKey: "test-key"},
		ProfileID:   "profile-1",
		ProfileName: "테스트 서버",
		ModelIDs:    []string{"local-agent"},
		ScenarioIDs: []string{"records-conflict-recovery"},
		Repetitions: 1,
	})
	if err != nil {
		t.Fatalf("StartAgenticEvaluation() error = %v", err)
	}
	select {
	case event := <-finished:
		if event.Status != "completed" {
			t.Fatalf("finished event = %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agentic evaluation did not finish")
	}
	opened, err := app.OpenAgenticEvaluation(created.ID)
	if err != nil {
		t.Fatalf("OpenAgenticEvaluation() error = %v", err)
	}
	if opened.Status != "completed" || len(opened.Runs) != 1 {
		t.Fatalf("evaluation = %#v", opened)
	}
	run := opened.Runs[0]
	if run.Status != "success" || run.Result == nil || !run.Result.Passed {
		t.Fatalf("run = %#v", run)
	}
	if len(run.Actions) != len(responses) || run.Actions[2].Status != "error" || len(run.StateChanges) != 1 {
		t.Fatalf("actions = %#v", run.Actions)
	}
	if run.Usage == nil || run.Usage.TotalTokens != len(responses)*14 {
		t.Fatalf("usage = %#v", run.Usage)
	}
}

func TestAgenticEvaluationSeparatesConnectionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "temporary upstream failure", http.StatusBadGateway)
	}))
	defer server.Close()

	app := NewApp()
	app.agenticEvaluations = newAgenticEvaluationStore(t.TempDir())
	finished := make(chan AgenticEvaluationEvent, 1)
	app.agenticEventSink = func(event AgenticEvaluationEvent) {
		if event.Type == "finished" {
			finished <- event
		}
	}
	created, err := app.StartAgenticEvaluation(AgenticEvaluationStartRequest{
		Profile: ConnectionProfile{BaseURL: server.URL, APIKey: "test-key"}, ProfileID: "profile-1", ProfileName: "테스트 서버",
		ModelIDs: []string{"local-agent"}, ScenarioIDs: []string{"records-no-change"}, Repetitions: 1,
	})
	if err != nil {
		t.Fatalf("StartAgenticEvaluation() error = %v", err)
	}
	select {
	case event := <-finished:
		if event.Status != "completed" {
			t.Fatalf("finished event = %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agentic evaluation did not finish")
	}
	opened, err := app.OpenAgenticEvaluation(created.ID)
	if err != nil {
		t.Fatalf("OpenAgenticEvaluation() error = %v", err)
	}
	if opened.Runs[0].Status != "connection_error" || opened.Runs[0].Result == nil || opened.Runs[0].Result.Outcome != "connection_error" {
		t.Fatalf("connection failure run = %#v", opened.Runs[0])
	}
}

func TestAgenticEvaluationSeparatesRepeatedFormatFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		encoded, _ := json.Marshal("설명 문장만 응답합니다")
		fmt.Fprintf(writer, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", encoded)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	app := NewApp()
	app.agenticEvaluations = newAgenticEvaluationStore(t.TempDir())
	finished := make(chan AgenticEvaluationEvent, 1)
	app.agenticEventSink = func(event AgenticEvaluationEvent) {
		if event.Type == "finished" {
			finished <- event
		}
	}
	created, err := app.StartAgenticEvaluation(AgenticEvaluationStartRequest{
		Profile: ConnectionProfile{BaseURL: server.URL, APIKey: "test-key"}, ProfileID: "profile-1", ProfileName: "테스트 서버",
		ModelIDs: []string{"local-agent"}, ScenarioIDs: []string{"records-no-change"}, Repetitions: 1,
	})
	if err != nil {
		t.Fatalf("StartAgenticEvaluation() error = %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("agentic evaluation did not finish")
	}
	opened, err := app.OpenAgenticEvaluation(created.ID)
	if err != nil {
		t.Fatalf("OpenAgenticEvaluation() error = %v", err)
	}
	run := opened.Runs[0]
	if run.Status != "failed" || run.Result == nil || run.Result.Outcome != "format_error" || len(run.Actions) != maxAgenticInvalidActions {
		t.Fatalf("format failure run = %#v", run)
	}
}
