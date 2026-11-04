package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

//go:embed frontend/src/datasets/domainDecisionSuite.json
var decisionBuiltinJSON []byte

const decisionScoringVersion = "business-domain-ranking-v2"
const decisionEventName = "decision-benchmark:event"

type DecisionOption struct {
	Key          string            `json:"key"`
	Descriptions map[string]string `json:"descriptions"`
}
type DecisionTask struct {
	Context      map[string]string `json:"context,omitempty"`
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Name         string            `json:"name"`
	Instructions map[string]string `json:"instructions"`
	Options      []DecisionOption  `json:"options"`
}
type DecisionCase struct {
	Instructions map[string]string `json:"instructions,omitempty"`
	ID           string            `json:"id"`
	PairID       string            `json:"pairID"`
	TaskID       string            `json:"taskID"`
	Language     string            `json:"language"`
	Text         string            `json:"text"`
	Expected     string            `json:"expected"`
	ScenarioID   string            `json:"scenarioID,omitempty"`
	Rationale    string            `json:"rationale,omitempty"`
	Candidate    string            `json:"candidate,omitempty"`
	ReviewStatus string            `json:"reviewStatus,omitempty"`
}
type DecisionSuite struct {
	QuestionStyle  string         `json:"questionStyle,omitempty"`
	SourceHash     string         `json:"sourceHash,omitempty"`
	Format         string         `json:"format"`
	Version        int            `json:"version"`
	ID             string         `json:"id,omitempty"`
	Revision       string         `json:"revision,omitempty"`
	Name           string         `json:"name"`
	Source         string         `json:"source"`
	ReviewStatus   string         `json:"reviewStatus,omitempty"`
	SourceDocument string         `json:"sourceDocument,omitempty"`
	ScoringVersion string         `json:"scoringVersion,omitempty"`
	Tasks          []DecisionTask `json:"tasks"`
	Cases          []DecisionCase `json:"cases"`
}
type DecisionTarget struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Runtime     string `json:"runtime"`
	BaseURL     string `json:"baseURL"`
	Model       string `json:"model"`
	Environment string `json:"environment"`
	Enabled     bool   `json:"enabled"`
}
type DecisionStartRequest struct {
	ExperimentName string            `json:"experimentName,omitempty"`
	Suite          DecisionSuite     `json:"suite"`
	Types          []string          `json:"types"`
	Targets        []DecisionTarget  `json:"targets"`
	Tokens         map[string]string `json:"tokens"`
	TimeoutSeconds int               `json:"timeoutSeconds"`
}
type DecisionCaseResult struct {
	RankingRecall       *float64           `json:"rankingRecall,omitempty"`
	RelevantProbability *float64           `json:"relevantProbability,omitempty"`
	NDCG                *float64           `json:"ndcg,omitempty"`
	TargetID            string             `json:"targetID"`
	CaseID              string             `json:"caseID"`
	TaskID              string             `json:"taskID"`
	Status              string             `json:"status"`
	Expected            string             `json:"expected"`
	Predicted           string             `json:"predicted"`
	Correct             bool               `json:"correct"`
	LabelValid          bool               `json:"labelValid"`
	ProbabilityValid    bool               `json:"probabilityValid"`
	ProbabilityError    string             `json:"probabilityError,omitempty"`
	Probabilities       map[string]float64 `json:"probabilities,omitempty"`
	ExpectedScore       float64            `json:"expectedScore"`
	Error               string             `json:"error,omitempty"`
	StartedAt           string             `json:"startedAt"`
	FinishedAt          string             `json:"finishedAt"`
	DurationMs          int64              `json:"durationMs"`
	Truncated           *bool              `json:"truncated"`
	ReturnedModel       string             `json:"returnedModel"`
	RoutingModel        string             `json:"routingModel"`
	ModelWarning        string             `json:"modelWarning,omitempty"`
	Raw                 json.RawMessage    `json:"raw,omitempty"`
}
type DecisionBenchmark struct {
	ExperimentName string                     `json:"experimentName,omitempty"`
	Imported       bool                       `json:"imported"`
	OriginID       string                     `json:"originID,omitempty"`
	ID             string                     `json:"id"`
	SchemaVersion  int                        `json:"schemaVersion"`
	ScoringVersion string                     `json:"scoringVersion"`
	Fingerprint    string                     `json:"fingerprint"`
	Suite          DecisionSuite              `json:"suite"`
	Targets        []DecisionTarget           `json:"targets"`
	Types          []string                   `json:"types"`
	TimeoutSeconds int                        `json:"timeoutSeconds"`
	Status         string                     `json:"status"`
	CreatedAt      string                     `json:"createdAt"`
	UpdatedAt      string                     `json:"updatedAt"`
	Error          string                     `json:"error,omitempty"`
	Results        []DecisionCaseResult       `json:"results"`
	Warmups        []DecisionCaseResult       `json:"warmups"`
	Summaries      []DecisionBenchmarkSummary `json:"summaries"`
}
type DecisionBenchmarkEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	Error     string `json:"error,omitempty"`
}

func (a *App) GetDefaultDecisionSuite() DecisionSuite {
	var s DecisionSuite
	_ = json.Unmarshal(decisionBuiltinJSON, &s)
	return s
}
func decisionTask(s DecisionSuite, id string) (DecisionTask, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return DecisionTask{}, false
}
func validateDecisionSuite(s DecisionSuite) error {
	if s.Format != "decision-benchmark-suite" || s.Version != 1 || strings.TrimSpace(s.Name) == "" || len(s.Tasks) == 0 || len(s.Cases) == 0 || len(s.Cases) > 10000 {
		return errors.New("올바른 Decision 테스트셋이 아닙니다")
	}
	tasks := map[string]bool{}
	ids := map[string]bool{}
	pairs := map[string]bool{}
	for _, t := range s.Tasks {
		if t.ID == "" || tasks[t.ID] || strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Instructions["ko"]) == "" || len(t.Options) < 2 || len(t.Options) > 26 {
			return errors.New("과제 ID·지시문·선택지를 확인해 주세요")
		}
		tasks[t.ID] = true
		keys := map[string]bool{}
		for i, o := range t.Options {
			if strings.TrimSpace(o.Key) == "" || keys[o.Key] || strings.TrimSpace(o.Descriptions["ko"]) == "" {
				return errors.New("선택지 키·설명이 비어 있거나 중복됩니다")
			}
			keys[o.Key] = true
			if t.Type == "score" && o.Key != strconv.Itoa(i) {
				return errors.New("등급은 0부터 순서대로 지정해야 합니다")
			}
		}
		switch t.Type {
		case "choice", "score", "ranking":
		case "noul":
			if len(keys) != 2 || !keys["false"] || !keys["true"] {
				return errors.New("noul은 false/true 선택지가 필요합니다")
			}
		default:
			return errors.New("알 수 없는 평가 유형입니다")
		}
	}
	for _, c := range s.Cases {
		t, ok := decisionTask(s, c.TaskID)
		if c.ID == "" || ids[c.ID] || !ok || c.Language != "ko" || strings.TrimSpace(c.Text) == "" {
			return fmt.Errorf("문항 %s의 ID·과제·한국어 입력을 확인해 주세요", c.ID)
		}
		ids[c.ID] = true
		valid := false
		for _, o := range t.Options {
			if o.Key == c.Expected {
				valid = true
			}
		}
		if t.Type == "ranking" {
			labels := strings.Split(c.Expected, "|")
			seen := map[string]bool{}
			valid = len(labels) >= 2
			for _, label := range labels {
				found := false
				for _, o := range t.Options {
					if o.Key == label {
						found = true
					}
				}
				if !found || seen[label] {
					valid = false
				}
				seen[label] = true
			}
		}
		if !valid {
			return fmt.Errorf("문항 %s의 정답이 유효하지 않습니다", c.ID)
		}
		if c.PairID != "" {
			key := c.TaskID + "\x00" + c.PairID
			if pairs[key] {
				return errors.New("대응 문항 ID가 중복됩니다")
			}
			pairs[key] = true
		}
	}
	return nil
}
func validateDecisionTarget(t DecisionTarget) error {
	u, e := url.Parse(strings.TrimSpace(t.BaseURL))
	endpoint := "/v1/systemone"
	if t.Runtime == "nimble" {
		endpoint = "/decide"
	}
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(strings.TrimRight(u.Path, "/"), endpoint) || strings.TrimSpace(t.Model) == "" || t.ID == "" {
		return errors.New("서버 기본 URL과 모델 ID를 확인해 주세요. 인증 정보는 URL에 넣지 않습니다")
	}
	return nil
}
func decisionFingerprint(s DecisionSuite, types []string) string {
	return decisionFingerprintForVersion(s, types, decisionScoringVersion)
}
func decisionFingerprintForVersion(s DecisionSuite, types []string, version string) string {
	b, _ := json.Marshal(struct {
		Suite   DecisionSuite
		Types   []string
		Scoring string
	}{s, types, version})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func buildDecisionRequest(t DecisionTarget, task DecisionTask, c DecisionCase) ([]byte, error) {
	var criteria any
	if task.Type == "score" {
		v := []string{}
		for _, o := range task.Options {
			v = append(v, o.Descriptions["ko"])
		}
		criteria = v
	} else {
		v := map[string]string{}
		for _, o := range task.Options {
			v[o.Key] = o.Descriptions["ko"]
		}
		criteria = v
	}
	body := c.Text
	if context := strings.TrimSpace(task.Context["ko"]); context != "" {
		body = context + "\n\n" + c.Text
	}
	instructions := task.Instructions["ko"]
	if override := strings.TrimSpace(c.Instructions["ko"]); override != "" {
		instructions = override
	}
	wireType := task.Type
	if wireType == "ranking" {
		wireType = "choice"
	}
	return json.Marshal(map[string]any{"model": strings.TrimSpace(t.Model), "state": map[string]string{"body": body}, "questions": map[string]any{"decision": map[string]any{"type": wireType, "instructions": instructions, "criteria": criteria}}})
}
