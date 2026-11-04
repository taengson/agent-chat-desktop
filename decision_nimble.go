package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func decisionAPIPath(target DecisionTarget) string {
	if target.Runtime == "nimble" {
		return "/decide"
	}
	return "/v1/systemone"
}

func buildNimbleRequest(task DecisionTask, item DecisionCase) ([]byte, error) {
	instructions := strings.TrimSpace(task.Instructions["ko"])
	if override := strings.TrimSpace(item.Instructions["ko"]); override != "" {
		instructions = override
	}
	context := item.Text
	if prefix := strings.TrimSpace(task.Context["ko"]); prefix != "" {
		context = prefix + "\n\n" + context
	}

	field := map[string]any{"description": instructions}
	scoreFields := []string{}
	switch task.Type {
	case "noul":
		field["type"] = "boolean"
		booleanDescriptions := []string{}
		for _, option := range task.Options {
			if description := strings.TrimSpace(option.Descriptions["ko"]); description != "" {
				booleanDescriptions = append(booleanDescriptions, option.Key+": "+description)
			}
		}
		if len(booleanDescriptions) > 0 {
			field["description"] = instructions + "\n\n" + strings.Join(booleanDescriptions, "\n")
		}
	case "choice", "ranking", "score":
		field["type"] = "enum"
		choices := make([]string, 0, len(task.Options))
		choiceDescriptions := make(map[string]string, len(task.Options))
		for _, option := range task.Options {
			choices = append(choices, option.Key)
			choiceDescriptions[option.Key] = option.Descriptions["ko"]
		}
		field["choices"] = choices
		field["choice_descriptions"] = choiceDescriptions
		if task.Type == "score" {
			scoreFields = append(scoreFields, "decision")
		}
	default:
		return nil, fmt.Errorf("Nimble에서 지원하지 않는 평가 유형입니다: %s", task.Type)
	}

	return json.Marshal(map[string]any{
		"context":      context,
		"schema":       map[string]any{"decision": field},
		"score_fields": scoreFields,
	})
}

type nimbleFieldResult struct {
	Prediction      json.RawMessage    `json:"prediction"`
	Probabilities   map[string]float64 `json:"probabilities"`
	ProbabilityTrue *float64           `json:"probability_true"`
}

type nimbleResponse struct {
	Model  string `json:"model"`
	Result struct {
		Output map[string]json.RawMessage   `json:"output"`
		Fields map[string]nimbleFieldResult `json:"fields"`
	} `json:"result"`
}

func adaptNimbleResponse(raw []byte, targetModel string, task DecisionTask) ([]byte, error) {
	var payload nimbleResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("Nimble 서버 응답이 올바른 JSON이 아닙니다")
	}
	field, ok := payload.Result.Fields["decision"]
	if !ok {
		return nil, errors.New("Nimble 응답에 result.fields.decision이 없습니다")
	}
	if len(field.Probabilities) == 0 {
		return nil, errors.New("Nimble 응답에 판단 확률이 없습니다")
	}

	wireType := task.Type
	if wireType == "ranking" {
		wireType = "choice"
	}
	answer := map[string]any{
		"type":          wireType,
		"probabilities": field.Probabilities,
	}
	switch task.Type {
	case "noul":
		probabilityTrue := field.ProbabilityTrue
		if probabilityTrue == nil {
			if value, exists := field.Probabilities["true"]; exists {
				probabilityTrue = &value
			}
		}
		if probabilityTrue == nil || !validDecisionNumber(*probabilityTrue) {
			return nil, errors.New("Nimble 응답에 유효한 probability_true가 없습니다")
		}
		answer["noul"] = *probabilityTrue
	case "choice", "ranking":
		prediction := field.Prediction
		if len(prediction) == 0 {
			prediction = payload.Result.Output["decision"]
		}
		var choice string
		if len(prediction) == 0 || json.Unmarshal(prediction, &choice) != nil || strings.TrimSpace(choice) == "" {
			return nil, errors.New("Nimble 응답에 유효한 선택 결과가 없습니다")
		}
		answer["choice"] = choice
	case "score":
		// The benchmark computes the expected ordinal score from the returned probabilities.
	default:
		return nil, fmt.Errorf("Nimble에서 지원하지 않는 평가 유형입니다: %s", task.Type)
	}

	model := strings.TrimSpace(payload.Model)
	if model == "" {
		model = strings.TrimSpace(targetModel)
	}
	return json.Marshal(map[string]any{
		"model":   model,
		"routing": map[string]string{"model": model},
		"answers": map[string]any{"decision": answer},
	})
}
