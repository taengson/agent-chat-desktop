package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

type DecisionModelCatalog struct {
	Models    []string `json:"models"`
	Available bool     `json:"available"`
	Message   string   `json:"message"`
	Source    string   `json:"source"`
}

// Model discovery is separate from inference: it never loads a model or sends dataset text.
func (a *App) GetDecisionModelCatalog(target DecisionTarget, token string) (DecisionModelCatalog, error) {
	return getDecisionModelCatalog(a.applicationContext(), target, token)
}
func decisionCatalogGET(ctx context.Context, target DecisionTarget, token, path string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(strings.TrimSpace(target.BaseURL), "/")+path, nil)
	if e != nil {
		return nil, 0, errors.New("서버 주소를 확인해 주세요")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return nil, 0, errors.New("모델 목록을 가져올 수 없습니다. 서버 주소와 실행 상태를 확인해 주세요")
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if e != nil || len(data) > 1024*1024 {
		return nil, resp.StatusCode, errors.New("모델 목록 응답을 읽을 수 없습니다")
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, resp.StatusCode, errors.New("모델 목록 조회에 필요한 API 키·접근 권한을 확인해 주세요")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return data, resp.StatusCode, nil
	}
	if resp.StatusCode == 404 || resp.StatusCode == 405 {
		return data, resp.StatusCode, nil
	}
	return nil, resp.StatusCode, fmt.Errorf("모델 목록 조회 실패 (HTTP %d)", resp.StatusCode)
}
func getDecisionModelCatalog(ctx context.Context, target DecisionTarget, token string) (DecisionModelCatalog, error) {
	out := DecisionModelCatalog{Models: []string{}}
	check := target
	check.Model = "catalog-probe"
	if e := validateDecisionTarget(check); e != nil {
		return out, e
	}
	if target.Runtime == "ollama" {
		data, status, e := decisionCatalogGET(ctx, target, token, "/api/tags")
		if e != nil {
			return out, e
		}
		if status != 200 {
			return out, errors.New("Ollama 설치 모델 목록을 조회할 수 없습니다. 서버 프로그램과 주소를 확인해 주세요")
		}
		var payload struct {
			Models []struct {
				Name         string   `json:"name"`
				Model        string   `json:"model"`
				Capabilities []string `json:"capabilities"`
			} `json:"models"`
		}
		if e = json.Unmarshal(data, &payload); e != nil {
			return out, errors.New("Ollama 모델 목록 형식이 올바르지 않습니다")
		}
		unknown := false
		for _, m := range payload.Models {
			decision := false
			for _, c := range m.Capabilities {
				if c == "decision" {
					decision = true
				}
			}
			if len(m.Capabilities) == 0 {
				decision = true
				unknown = true
			}
			if decision {
				name := m.Name
				if name == "" {
					name = m.Model
				}
				if strings.TrimSpace(name) != "" {
					out.Models = append(out.Models, name)
				}
			}
		}
		out.Available = true
		out.Source = "ollama-installed"
		out.Message = "설치된 Decision 모델을 선택하세요. 태그까지 그대로 사용합니다"
		if unknown {
			out.Message = "서버가 일부 모델의 Decision 지원 여부를 제공하지 않습니다. 선택 후 연결 테스트로 확인하세요"
		}
		if len(out.Models) == 0 {
			out.Message = "설치된 Decision 모델이 없습니다. 서버에서 모델을 설치한 뒤 목록을 새로고침하세요"
		}
	} else if target.Runtime == "nimble" {
		data, status, e := decisionCatalogGET(ctx, target, token, "/health")
		if e != nil {
			return out, e
		}
		var health struct {
			Status string `json:"status"`
			Model  string `json:"model"`
		}
		if status == http.StatusOK && json.Unmarshal(data, &health) == nil && health.Status == "ok" && strings.TrimSpace(health.Model) != "" {
			out.Models = append(out.Models, strings.TrimSpace(health.Model))
			out.Available = true
			out.Source = "server-loaded"
			out.Message = "Nimble 서버가 현재 로드한 모델입니다"
		} else {
			out.Message = "Nimble 서버에서 모델 ID를 확인할 수 없습니다. 서버에 설정된 정확한 모델 ID를 직접 입력하세요"
		}
	} else {
		data, status, e := decisionCatalogGET(ctx, target, token, "/v1/models")
		if e != nil {
			return out, e
		}
		if status >= 200 && status < 300 {
			var payload struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if e = json.Unmarshal(data, &payload); e != nil || payload.Data == nil {
				return out, errors.New("서버 모델 목록 형식이 올바르지 않습니다")
			}
			for _, m := range payload.Data {
				if strings.TrimSpace(m.ID) != "" {
					out.Models = append(out.Models, m.ID)
				}
			}
			out.Available = true
			out.Source = "server-models"
			out.Message = "서버가 제공한 모델 목록입니다. 선택 후 연결 테스트로 Decision 응답을 확인하세요"
		} else {
			// Laya serves /health rather than a standard model catalog. Only report IDs actually returned by it.
			data, status, e = decisionCatalogGET(ctx, target, token, "/health")
			if e != nil {
				return out, e
			}
			var health struct {
				Status string   `json:"status"`
				Loaded []string `json:"loaded"`
			}
			if status == 200 && json.Unmarshal(data, &health) == nil && health.Status == "ok" && health.Loaded != nil {
				out.Models = append(out.Models, health.Loaded...)
				out.Available = true
				out.Source = "server-loaded"
				out.Message = "서버가 현재 로드한 모델 목록입니다. 로드되지 않은 모델이 있다면 직접 입력할 수 있습니다"
			} else {
				out.Message = "이 서버는 조회 가능한 모델 목록을 제공하지 않습니다. 서버에 설정된 정확한 모델 ID를 직접 입력하세요"
			}
		}
	}
	sort.Strings(out.Models)
	names := []string{}
	for _, name := range out.Models {
		if len(names) == 0 || names[len(names)-1] != name {
			names = append(names, name)
		}
	}
	out.Models = names
	return out, nil
}
func ensureDecisionModelInstalled(ctx context.Context, target DecisionTarget, token string) error {
	if target.Runtime != "ollama" && target.Runtime != "nimble" {
		return nil
	}
	catalog, e := getDecisionModelCatalog(ctx, target, token)
	if e != nil {
		return e
	}
	if target.Runtime == "nimble" && !catalog.Available {
		return nil
	}
	model := strings.TrimSpace(target.Model)
	for _, name := range catalog.Models {
		if name == model {
			return nil
		}
	}
	alternatives := []string{}
	for _, name := range catalog.Models {
		if strings.Split(name, ":")[0] == strings.Split(model, ":")[0] {
			alternatives = append(alternatives, name)
		}
	}
	if len(alternatives) > 0 {
		return fmt.Errorf("모델 %q는 설치된 정확한 태그와 다릅니다. 모델 목록에서 %s을 선택해 주세요", model, strings.Join(alternatives, ", "))
	}
	if target.Runtime == "nimble" {
		return fmt.Errorf("모델 ID %q가 Nimble 서버의 로드 모델과 다릅니다. %s를 선택해 주세요", model, strings.Join(catalog.Models, ", "))
	}
	return fmt.Errorf("모델 %q가 설치된 Decision 모델 목록에 없습니다. 모델 목록을 새로고침해 선택해 주세요", model)
}

var decisionOptionBudgetError = regexp.MustCompile(`(?i)options exceed (?:the )?([0-9]+)-token budget`)
var decisionMemoryError = regexp.MustCompile(`(?i)model requires ([0-9.]+) GiB but only ([0-9.]+) GiB are available`)

func decisionHTTPError(status int, raw []byte, t DecisionTarget) error {
	var serverError struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &serverError)
	if status == 400 {
		if match := decisionOptionBudgetError.FindStringSubmatch(serverError.Error); match != nil {
			return fmt.Errorf("모델의 선택지 입력 한도(%s토큰)를 초과했습니다. 서버·모델의 입력 제한을 확인하거나 모든 비교 대상에 적용할 공통 기준 길이를 줄여야 합니다", match[1])
		}
	}
	if status == 500 {
		if match := decisionMemoryError.FindStringSubmatch(serverError.Error); match != nil {
			return fmt.Errorf("서버 메모리가 부족합니다. 모델에 %sGiB가 필요하지만 사용 가능한 메모리는 %sGiB입니다. 서버 메모리를 확보한 뒤 다시 시도해 주세요", match[1], match[2])
		}
	}
	switch status {
	case 401, 403:
		return errors.New("API 키 또는 서버 접근 권한을 확인해 주세요")
	case 404:
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &body)
		message := strings.ToLower(body.Error)
		if strings.Contains(message, "model") && strings.Contains(message, "not found") {
			return fmt.Errorf("서버에서 모델 %q를 찾을 수 없습니다. 설치된 정확한 모델 태그를 목록에서 선택해 주세요", strings.TrimSpace(t.Model))
		}
		return fmt.Errorf("서버에 Decision API 경로 %s가 없습니다. 서버 프로그램과 기본 주소를 확인해 주세요", decisionAPIPath(t))
	default:
		return fmt.Errorf("서버 HTTP 오류 %d", status)
	}
}
