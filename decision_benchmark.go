package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wailsapp/wails/v3/pkg/application"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *App) ListDecisionBenchmarks() ([]DecisionBenchmark, error) { return a.decisions.list() }
func (a *App) OpenDecisionBenchmark(id string) (DecisionBenchmark, error) {
	return a.decisions.open(id)
}
func (a *App) DeleteDecisionBenchmark(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.decisionActiveID == id {
		return errors.New("실행 중인 기록은 삭제할 수 없습니다")
	}
	return a.decisions.delete(id)
}
func (a *App) CancelDecisionBenchmark(id string) bool { return a.CancelChat(id) }
func (a *App) GetActiveDecisionBenchmark() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.decisionActiveID
}
func (a *App) CheckDecisionConnection(target DecisionTarget, token string, timeoutSeconds int) (DecisionCaseResult, error) {
	if err := validateDecisionTarget(target); err != nil {
		return DecisionCaseResult{}, err
	}
	if timeoutSeconds != 60 && timeoutSeconds != 180 {
		return DecisionCaseResult{}, errors.New("요청 제한은 60초 또는 180초입니다")
	}
	if err := ensureDecisionModelInstalled(a.applicationContext(), target, token); err != nil {
		return DecisionCaseResult{}, err
	}
	// Connectivity probes must be independent of the dataset and its option budget.
	task := DecisionTask{ID: "connection-probe", Type: "noul", Instructions: map[string]string{"ko": "Does the text contain a greeting?"}, Options: []DecisionOption{
		{Key: "false", Descriptions: map[string]string{"ko": "No greeting"}},
		{Key: "true", Descriptions: map[string]string{"ko": "A greeting"}},
	}}
	item := DecisionCase{ID: "connection-probe", TaskID: task.ID, Language: "ko", Text: "Hello", Expected: "true"}
	return callDecision(a.applicationContext(), target, token, task, item, timeoutSeconds)
}
func (a *App) StartDecisionBenchmark(req DecisionStartRequest) (DecisionBenchmark, error) {
	req.ExperimentName = strings.TrimSpace(req.ExperimentName)
	if len([]rune(req.ExperimentName)) > 100 {
		return DecisionBenchmark{}, errors.New("실험 이름은 100자 이하여야 합니다")
	}
	if req.ExperimentName == "" {
		req.ExperimentName = req.Suite.Name + " 실험"
	}
	if err := validateDecisionSuite(req.Suite); err != nil {
		return DecisionBenchmark{}, err
	}
	if req.TimeoutSeconds != 60 && req.TimeoutSeconds != 180 {
		return DecisionBenchmark{}, errors.New("요청 제한은 60초 또는 180초입니다")
	}
	selected := map[string]bool{}
	for _, typ := range req.Types {
		if typ != "choice" && typ != "noul" && typ != "score" && typ != "ranking" {
			return DecisionBenchmark{}, errors.New("알 수 없는 평가 유형")
		}
		selected[typ] = true
	}
	types := []string{}
	for _, typ := range []string{"choice", "ranking", "noul", "score"} {
		if selected[typ] {
			types = append(types, typ)
		}
	}
	if len(types) == 0 {
		return DecisionBenchmark{}, errors.New("평가 유형을 선택해 주세요")
	}
	targets := []DecisionTarget{}
	targetIDs := map[string]bool{}
	for _, t := range req.Targets {
		t.BaseURL = strings.TrimSpace(t.BaseURL)
		t.Model = strings.TrimSpace(t.Model)
		t.Name = t.Model
		t.ID = strings.TrimSpace(t.ID)
		if !t.Enabled {
			continue
		}
		if err := validateDecisionTarget(t); err != nil {
			return DecisionBenchmark{}, err
		}
		if targetIDs[t.ID] {
			return DecisionBenchmark{}, errors.New("대상 ID가 중복됩니다")
		}
		targetIDs[t.ID] = true
		if err := ensureDecisionModelInstalled(a.applicationContext(), t, req.Tokens[t.ID]); err != nil {
			return DecisionBenchmark{}, err
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 || len(targets) > 12 {
		return DecisionBenchmark{}, errors.New("1~12개 대상을 선택해 주세요")
	}
	b := DecisionBenchmark{ExperimentName: req.ExperimentName, ID: newConversationID(), SchemaVersion: 1, ScoringVersion: decisionScoringVersion, Fingerprint: decisionFingerprint(req.Suite, types), Suite: req.Suite, Targets: targets, Types: types, TimeoutSeconds: req.TimeoutSeconds, Status: "running", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Results: []DecisionCaseResult{}, Warmups: []DecisionCaseResult{}}
	for _, t := range targets {
		for _, task := range b.Suite.Tasks {
			if !selected[task.Type] {
				continue
			}
			for _, c := range b.Suite.Cases {
				if c.TaskID == task.ID {
					b.Results = append(b.Results, DecisionCaseResult{TargetID: t.ID, CaseID: c.ID, TaskID: task.ID, Expected: c.Expected, Status: "pending"})
				}
			}
		}
	}
	if len(b.Results) == 0 || len(b.Results) > 12000 {
		return DecisionBenchmark{}, errors.New("한 실행의 문항 수는 1~12000개입니다")
	}
	ctx, cancel := context.WithCancel(a.applicationContext())
	a.mu.Lock()
	if a.decisionActiveID != "" || a.decisionStopping {
		a.mu.Unlock()
		cancel()
		return DecisionBenchmark{}, errors.New("Decision 평가가 이미 실행 중입니다")
	}
	a.decisionWG.Add(1)
	a.decisionActiveID = b.ID
	a.cancels[b.ID] = cancel
	a.mu.Unlock()
	release := func() { a.mu.Lock(); delete(a.cancels, b.ID); a.decisionActiveID = ""; a.mu.Unlock(); cancel() }
	if err := a.decisions.prepare(); err != nil {
		release()
		a.decisionWG.Done()
		return DecisionBenchmark{}, err
	}
	b.UpdatedAt = b.CreatedAt
	b.Summaries = summarizeDecision(b)
	if err := a.decisions.save(b); err != nil {
		release()
		a.decisionWG.Done()
		return DecisionBenchmark{}, err
	}
	tokens := map[string]string{}
	for k, v := range req.Tokens {
		tokens[k] = v
	}
	// Give the worker its own snapshot; the returned record must not share mutable slices.
	snapshot, _ := json.Marshal(b)
	var worker DecisionBenchmark
	if err := json.Unmarshal(snapshot, &worker); err != nil {
		release()
		a.decisionWG.Done()
		return DecisionBenchmark{}, err
	}
	go func() { defer a.decisionWG.Done(); defer release(); a.runDecision(ctx, worker, tokens) }()
	return b, nil
}
func callDecision(parent context.Context, t DecisionTarget, token string, task DecisionTask, c DecisionCase, seconds int) (DecisionCaseResult, error) {
	r := DecisionCaseResult{TargetID: t.ID, CaseID: c.ID, TaskID: task.ID, Expected: c.Expected, Status: "error"}
	start := time.Now()
	r.StartedAt = start.UTC().Format(time.RFC3339Nano)
	var body []byte
	var err error
	if t.Runtime == "nimble" {
		body, err = buildNimbleRequest(task, c)
	} else {
		body, err = buildDecisionRequest(t, task, c)
	}
	if err != nil {
		return r, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+decisionAPIPath(t), strings.NewReader(string(body)))
	if err != nil {
		return r, errors.New("요청을 만들 수 없습니다")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		err = errors.New("서버 요청 실패 또는 시간 초과")
		if parent.Err() != nil {
			err = parent.Err()
		}
	} else {
		defer resp.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
		switch {
		case e != nil:
			err = errors.New("서버 응답 수신 실패")
		case len(raw) > 1024*1024:
			err = errors.New("서버 응답 크기 제한 초과")
		case resp.StatusCode < 200 || resp.StatusCode >= 300:
			err = decisionHTTPError(resp.StatusCode, raw, t)
		default:
			if t.Runtime == "nimble" {
				raw, err = adaptNimbleResponse(raw, t.Model, task)
			}
			if err != nil {
				break
			}
			// Remove the in-memory bearer token even if a server echoes it.
			raw = redactDecisionToken(raw, token)
			parsed, e := scoreDecisionResponse(raw, task, c)
			parsed.TargetID = t.ID
			parsed.StartedAt = r.StartedAt
			r = parsed
			err = e
		}
	}
	r.DurationMs = time.Since(start).Milliseconds()
	r.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		r.Status = "error"
		r.Error = err.Error()
		if parent.Err() != nil {
			r.Status = "cancelled"
		}
	} else {
		r.Status = "completed"
	}
	if r.RoutingModel != "" && r.RoutingModel != strings.TrimSpace(t.Model) {
		r.ModelWarning = "요청 모델과 실제 라우팅 모델이 다릅니다"
	}
	return r, err
}
func (a *App) emitDecision(b DecisionBenchmark, typ string) {
	done := 0
	for _, r := range b.Results {
		if r.Status != "pending" {
			done++
		}
	}
	event := DecisionBenchmarkEvent{ID: b.ID, Type: typ, Completed: done, Total: len(b.Results), Error: b.Error}
	if a.decisionEventSink != nil {
		a.decisionEventSink(event)
	} else if application.Get() != nil {
		application.Get().Event.Emit(decisionEventName, event)
	}
}
func (a *App) runDecision(ctx context.Context, b DecisionBenchmark, tokens map[string]string) {
	save := func(typ string) bool {
		b.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		b.Summaries = summarizeDecision(b)
		if e := a.decisions.save(b); e != nil {
			b.Status = "failed"
			b.Error = "실행 기록 저장 실패: " + e.Error()
			a.emitDecision(b, "failed")
			return false
		}
		a.emitDecision(b, typ)
		return true
	}
	a.emitDecision(b, "started")
	cases := map[string]DecisionCase{}
	for _, c := range b.Suite.Cases {
		cases[c.ID] = c
	}
	stop := false
	anyFailure := false
	for _, target := range b.Targets {
		if ctx.Err() != nil {
			break
		}
		for _, task := range b.Suite.Tasks {
			first := -1
			for i, r := range b.Results {
				if r.TargetID == target.ID && r.TaskID == task.ID {
					first = i
					break
				}
			}
			if first < 0 {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			warm, e := callDecision(ctx, target, tokens[target.ID], task, cases[b.Results[first].CaseID], b.TimeoutSeconds)
			if e == nil && !warm.LabelValid {
				warm.Error = warm.ProbabilityError
				if warm.Error == "" {
					warm.Error = "응답을 채점할 수 없습니다"
				}
			}
			b.Warmups = append(b.Warmups, warm)
			if ctx.Err() != nil {
				break
			}
			if e != nil || !warm.LabelValid {
				anyFailure = true
				b.Error = "일부 평가 유형의 준비 요청이 실패했습니다"
				for i, r := range b.Results {
					if r.TargetID == target.ID && r.TaskID == task.ID && r.Status == "pending" {
						b.Results[i].Status = "skipped"
						b.Results[i].Error = "준비 요청 실패: " + warm.Error
					}
				}
				if !save("task-failed") {
					return
				}
				continue
			}
			if !save("warming") {
				return
			}
			for i, r := range b.Results {
				if r.TargetID != target.ID || r.TaskID != task.ID {
					continue
				}
				if ctx.Err() != nil {
					stop = true
					break
				}
				result, _ := callDecision(ctx, target, tokens[target.ID], task, cases[r.CaseID], b.TimeoutSeconds)
				b.Results[i] = result
				if !save("case") {
					return
				}
			}
			if stop {
				break
			}
		}

	}
	b.Status = "completed"
	if anyFailure {
		b.Status = "partial"
	}
	for _, r := range b.Results {
		if r.Status == "error" {
			b.Status = "partial"
		}
	}
	if ctx.Err() != nil {
		b.Status = "cancelled"
	}
	for i := range b.Results {
		if b.Results[i].Status == "pending" {
			b.Results[i].Status = "skipped"
		}
	}
	save(b.Status)
}
func (a *App) ExportDecisionBenchmark(id, format string) (string, error) {
	if format != "md" && format != "html" {
		return "", errors.New("Markdown 또는 HTML을 선택해 주세요")
	}
	b, e := a.decisions.open(id)
	if e != nil {
		return "", e
	}
	return decisionReport(b, format)
}
func (a *App) SaveDecisionReport(path, contents string) error { return saveTextExport(path, contents) }

// Imported reports are validated and stored as new local snapshots; duplicates keep their existing ID.
func (a *App) ImportDecisionReport(contents string) (DecisionBenchmark, error) {
	b, e := decodeDecisionRecord([]byte(contents))
	if e != nil {
		return b, e
	}
	if b.Status == "running" {
		return b, errors.New("실행 중 보고서는 가져올 수 없습니다")
	}
	records, e := a.decisions.list()
	if e != nil {
		return b, e
	}
	incoming := decisionRecordDigest(b)
	collision := false
	for _, existing := range records {
		if decisionRecordDigest(existing) == incoming {
			return existing, nil
		}
		if existing.ID == b.ID {
			collision = true
		}
	}
	b.OriginID = b.ID
	b.Imported = true
	if collision {
		b.ID = newConversationID()
	}
	if e = a.decisions.prepare(); e != nil {
		return b, e
	}
	return b, a.decisions.save(b)
}
func decisionRecordDigest(b DecisionBenchmark) string {
	b.ID = ""
	b.Imported = false
	b.OriginID = ""
	payload, _ := json.Marshal(b)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Comparison reports embed each full original record, enabling complete restoration.
func (a *App) ExportDecisionComparison(ids []string, format string) (string, error) {
	if format != "md" && format != "html" {
		return "", errors.New("Markdown 또는 HTML을 선택해 주세요")
	}
	comparison, e := a.CompareDecisionBenchmarks(ids)
	if e != nil {
		return "", e
	}
	var output strings.Builder
	fmt.Fprintf(&output, "# Decision Benchmark 비교\n\n공통 채점 가능 문항: %d\n\n%s\n\n", comparison.CommonCaseCount, comparison.Warning)
	for _, b := range comparison.Records {
		for _, s := range b.Summaries {
			data, _ := json.MarshalIndent(s, "", "  ")
			fmt.Fprintf(&output, "## %s / %s / %s\n\n~~~json\n%s\n~~~\n\n", b.ID, s.TargetID, s.TaskID, data)
		}
	}
	if format == "html" {
		output.Reset()
		output.WriteString(decisionHTMLReport(comparison.Records, "Decision Benchmark 비교 보고서", fmt.Sprintf("기록 간 공통 채점 가능 문항: %d · %s", comparison.CommonCaseCount, comparison.Warning)))
	}
	if format == "md" {
		text := neutralDecisionText(output.String())
		output.Reset()
		output.WriteString(text)
	}
	for _, id := range ids {
		b, e := a.decisions.open(id)
		if e != nil {
			return "", e
		}
		payload, e := json.Marshal(b)
		if e != nil {
			return "", e
		}
		output.WriteString("\n" + decisionRecordMarker + base64.StdEncoding.EncodeToString(payload) + " -->\n")
	}
	if output.Len() > maxDecisionRecordBytes {
		return "", errors.New("비교 보고서가 64MiB 한도를 넘었습니다")
	}
	return output.String(), nil
}
func (a *App) ImportDecisionReports(contents string) ([]DecisionBenchmark, error) {
	if len(contents) > maxDecisionRecordBytes {
		return nil, errors.New("보고서 크기 제한 초과")
	}
	pieces := strings.Split(contents, decisionRecordMarker)
	if len(pieces) < 2 || len(pieces) > 4 {
		return nil, errors.New("보고서는 1~3개 기록을 포함해야 합니다")
	}
	reports := []string{}
	for _, part := range pieces[1:] {
		end := strings.Index(part, " -->")
		if end < 0 {
			return nil, errors.New("복원 데이터 형식 오류")
		}
		report := decisionRecordMarker + part[:end] + " -->"
		b, e := decodeDecisionRecord([]byte(report))
		if e != nil {
			return nil, e
		}
		if b.Status == "running" {
			return nil, errors.New("실행 중 보고서는 가져올 수 없습니다")
		}
		reports = append(reports, report)
	}
	out := []DecisionBenchmark{}
	for _, report := range reports {
		b, e := a.ImportDecisionReport(report)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, nil
}

func redactDecisionToken(raw []byte, token string) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return []byte("null")
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case string:
			if token != "" {
				return strings.ReplaceAll(x, token, "[REDACTED]")
			}
			return x
		case map[string]any:
			m := map[string]any{}
			for k, item := range x {
				lower := strings.ToLower(k)
				if lower == "authorization" || lower == "api_key" || lower == "apikey" || lower == "access_token" || lower == "headers" {
					continue
				}
				key := k
				if token != "" {
					key = strings.ReplaceAll(k, token, "[REDACTED]")
				}
				m[key] = clean(item)
			}
			return m
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
			return x
		}
		return v
	}
	result, _ := json.Marshal(clean(value))
	return result
}
