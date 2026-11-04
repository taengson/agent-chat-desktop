package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func nimbleResponseFixture(task DecisionTask) []byte {
	probabilities := map[string]float64{}
	var prediction any
	switch task.Type {
	case "noul":
		prediction = true
		probabilities = map[string]float64{"false": 0.1, "true": 0.9}
	case "score":
		prediction = 2
		probabilities = map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7}
	default:
		prediction = "D01"
		probabilities = map[string]float64{"D01": 0.75, "D02": 0.25}
		if task.Type == "ranking" {
			prediction = "D01"
			probabilities = map[string]float64{"D01": 0.5, "D02": 0.5}
		}
	}
	probabilityTrue := 0.9
	result, _ := json.Marshal(map[string]any{
		"model": "Bespoke-Nimble-9B",
		"result": map[string]any{
			"output": map[string]any{"decision": prediction},
			"fields": map[string]any{"decision": map[string]any{
				"prediction": prediction, "probabilities": probabilities, "probability_true": probabilityTrue,
			}},
		},
		"latency_ms": 1.25,
	})
	return result
}

func TestNimbleRequestAndResponseAdapters(t *testing.T) {
	suite := decisionTestSuite()
	for _, testCase := range []struct {
		task      DecisionTask
		item      DecisionCase
		wantType  string
		wantScore bool
	}{
		{task: suite.Tasks[0], item: suite.Cases[0], wantType: "enum"},
		{task: suite.Tasks[1], item: suite.Cases[1], wantType: "boolean"},
		{task: suite.Tasks[2], item: suite.Cases[2], wantType: "enum", wantScore: true},
		{task: func() DecisionTask { task := suite.Tasks[0]; task.Type = "ranking"; return task }(), item: func() DecisionCase { item := suite.Cases[0]; item.Expected = "D01|D02"; return item }(), wantType: "enum"},
	} {
		task := testCase.task
		request, err := buildNimbleRequest(task, testCase.item)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Context string `json:"context"`
			Schema  map[string]struct {
				Type               string            `json:"type"`
				Description        string            `json:"description"`
				Choices            []string          `json:"choices"`
				ChoiceDescriptions map[string]string `json:"choice_descriptions"`
			} `json:"schema"`
			ScoreFields []string `json:"score_fields"`
		}
		if err := json.Unmarshal(request, &decoded); err != nil {
			t.Fatal(err)
		}
		field := decoded.Schema["decision"]
		wantDescription := task.Instructions["ko"]
		if task.Type == "noul" {
			wantDescription += "\n\nfalse: " + task.Options[0].Descriptions["ko"] + "\ntrue: " + task.Options[1].Descriptions["ko"]
		}
		if decoded.Context != testCase.item.Text || field.Type != testCase.wantType || field.Description != wantDescription {
			t.Fatalf("unexpected Nimble request: %s", request)
		}
		if testCase.wantType == "enum" && (len(field.Choices) != len(task.Options) || len(field.ChoiceDescriptions) != len(task.Options)) {
			t.Fatalf("enum criteria were not mapped: %s", request)
		}
		if testCase.wantScore != reflect.DeepEqual(decoded.ScoreFields, []string{"decision"}) {
			t.Fatalf("score_fields = %v, score task = %v", decoded.ScoreFields, testCase.wantScore)
		}

		adapted, err := adaptNimbleResponse(nimbleResponseFixture(task), "Bespoke-Nimble-9B", task)
		if err != nil {
			t.Fatal(err)
		}
		result, err := scoreDecisionResponse(adapted, task, testCase.item)
		if err != nil || !result.LabelValid || !result.ProbabilityValid {
			t.Fatalf("adapted response did not score: %s %+v %v", adapted, result, err)
		}
	}
}

func TestNimbleConnectionUsesHealthDecideAndBearerToken(t *testing.T) {
	var healthCalls, decideCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			healthCalls++
			if r.Header.Get("Authorization") != "Bearer secret" {
				t.Error("missing bearer token on health request")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","model":"Bespoke-Nimble-9B"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/decide":
			decideCalls++
			if r.Header.Get("Authorization") != "Bearer secret" {
				t.Error("missing bearer token on decision request")
			}
			var request struct {
				Context string `json:"context"`
				Schema  map[string]struct {
					Type string `json:"type"`
				} `json:"schema"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Context != "Hello" || request.Schema["decision"].Type != "boolean" {
				t.Errorf("unexpected decision request: %+v", request)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model":"Bespoke-Nimble-9B","result":{"output":{"decision":true},"fields":{"decision":{"prediction":true,"probabilities":{"false":0.1,"true":0.9},"probability_true":0.9}}},"latency_ms":1.25}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := DecisionTarget{ID: "nimble", Runtime: "nimble", BaseURL: server.URL, Model: "Bespoke-Nimble-9B", Enabled: true}
	catalog, err := getDecisionModelCatalog(context.Background(), target, "secret")
	if err != nil || !catalog.Available || !reflect.DeepEqual(catalog.Models, []string{"Bespoke-Nimble-9B"}) {
		t.Fatalf("Nimble model discovery failed: %+v %v", catalog, err)
	}
	a := NewApp()
	result, err := a.CheckDecisionConnection(target, "secret", 60)
	if err != nil || result.Status != "completed" || !result.LabelValid || !result.Correct {
		t.Fatalf("Nimble connection check failed: %+v %v", result, err)
	}
	if healthCalls != 2 || decideCalls != 1 {
		t.Fatalf("health calls = %d, decide calls = %d", healthCalls, decideCalls)
	}
}

func TestNimbleTargetRejectsEndpointInBaseURL(t *testing.T) {
	if err := validateDecisionTarget(DecisionTarget{ID: "nimble", Runtime: "nimble", BaseURL: "http://spark:12300/decide", Model: "Bespoke-Nimble-9B"}); err == nil {
		t.Fatal("accepted /decide as the base URL")
	}
}
