package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/taengson/agent-chat-desktop/internal/provider/openai"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const chatEventName = "chat:event"

const (
	benchmarkFirstOutputTimeout = 10 * time.Minute
	benchmarkOutputIdleTimeout  = 60 * time.Second
	chatFirstOutputTimeout      = 15 * time.Minute
	chatOutputIdleTimeout       = 5 * time.Minute
)

func normalizeReasoningEffort(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return value, nil
	default:
		return "", errors.New("올바르지 않은 추론 강도입니다")
	}
}

type streamTimeoutPolicy struct {
	firstOutputTimeout time.Duration
	outputIdleTimeout  time.Duration
	checkInterval      time.Duration
	firstOutputFailure string
	outputIdleFailure  string
}

var defaultBenchmarkStreamTimeoutPolicy = streamTimeoutPolicy{
	firstOutputTimeout: benchmarkFirstOutputTimeout,
	outputIdleTimeout:  benchmarkOutputIdleTimeout,
	checkInterval:      time.Second,
	firstOutputFailure: "벤치마크 응답이 10분 안에 시작되지 않았습니다",
	outputIdleFailure:  "벤치마크 출력이 60초 동안 멈췄습니다",
}

var defaultChatStreamTimeoutPolicy = streamTimeoutPolicy{
	firstOutputTimeout: chatFirstOutputTimeout,
	outputIdleTimeout:  chatOutputIdleTimeout,
	checkInterval:      time.Second,
	firstOutputFailure: "채팅 응답이 15분 안에 시작되지 않았습니다",
	outputIdleFailure:  "채팅 출력이 5분 동안 멈췄습니다",
}

// streamWatchdog allows an active stream to run without an absolute deadline.
// It only cancels requests that never begin or stop producing text.
type streamWatchdog struct {
	startedAt     time.Time
	cancel        context.CancelFunc
	policy        streamTimeoutPolicy
	firstOutputAt int64
	lastOutputAt  int64
	done          chan struct{}
	stopOnce      sync.Once

	mu         sync.RWMutex
	timeoutErr error
	stopped    bool
}

func newStreamWatchdog(startedAt time.Time, cancel context.CancelFunc, policy streamTimeoutPolicy) *streamWatchdog {
	return &streamWatchdog{
		startedAt: startedAt,
		cancel:    cancel,
		policy:    policy,
		done:      make(chan struct{}),
	}
}

func (w *streamWatchdog) recordOutput() {
	now := time.Now().UnixNano()
	w.mu.Lock()
	if w.firstOutputAt == 0 {
		w.firstOutputAt = now
	}
	w.lastOutputAt = now
	w.mu.Unlock()
}

func (w *streamWatchdog) timeoutError() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.timeoutErr
}

func (w *streamWatchdog) stop() {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		w.stopped = true
		w.mu.Unlock()
		close(w.done)
	})
}

func (w *streamWatchdog) watch(ctx context.Context) {
	interval := w.policy.checkInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case now := <-ticker.C:
			firstOutputAt, lastOutputAt := w.outputTimes()
			if firstOutputAt == 0 {
				if now.Sub(w.startedAt) >= w.policy.firstOutputTimeout {
					w.expire(w.firstOutputTimeoutError())
					return
				}
				continue
			}
			if now.Sub(time.Unix(0, lastOutputAt)) >= w.policy.outputIdleTimeout {
				w.expire(w.outputIdleTimeoutError())
				return
			}
		}
	}
}

func (w *streamWatchdog) firstOutputTimeoutError() error {
	if w.policy.firstOutputFailure != "" {
		return errors.New(w.policy.firstOutputFailure)
	}
	return errors.New("응답이 정해진 시간 안에 시작되지 않았습니다")
}

func (w *streamWatchdog) outputIdleTimeoutError() error {
	if w.policy.outputIdleFailure != "" {
		return errors.New(w.policy.outputIdleFailure)
	}
	return errors.New("응답 출력이 정해진 시간 동안 멈췄습니다")
}

func (w *streamWatchdog) outputTimes() (int64, int64) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.firstOutputAt, w.lastOutputAt
}

func (w *streamWatchdog) expire(err error) {
	w.mu.Lock()
	if w.stopped || w.timeoutErr != nil {
		w.mu.Unlock()
		return
	}
	w.timeoutErr = err
	w.mu.Unlock()
	w.cancel()
}

type ConnectionProfile struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
}

type Model struct {
	ID      string `json:"id"`
	OwnedBy string `json:"ownedBy,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	RequestID       string            `json:"requestID"`
	Profile         ConnectionProfile `json:"profile"`
	Model           string            `json:"model"`
	Messages        []ChatMessage     `json:"messages"`
	ReasoningEffort string            `json:"reasoningEffort,omitempty"`
	Benchmark       bool              `json:"benchmark"`
}

type ChatEvent struct {
	RequestID string           `json:"requestID"`
	Type      string           `json:"type"`
	Delta     string           `json:"delta,omitempty"`
	Usage     *TokenUsage      `json:"usage,omitempty"`
	Metrics   *ResponseMetrics `json:"metrics,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// TokenUsage is the token accounting reported by a compatible chat server for
// one completed request. Prompt and completion tokens are kept separately so
// the frontend can present total token usage without estimating tokens from
// text.
type TokenUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// ResponseMetrics is measured locally for each streaming response. The first
// token duration is zero when a server finishes without emitting text.
type ResponseMetrics struct {
	TotalDurationMs      int64 `json:"totalDurationMs"`
	FirstTokenDurationMs int64 `json:"firstTokenDurationMs"`
}

type App struct {
	ctx context.Context

	mu            sync.Mutex
	cancels       map[string]context.CancelFunc
	conversations *conversationStore
	profiles      *connectionProfileStore
	benchmarks    *modelBenchmarkStore
	syncV2        *benchmarkSyncV2Store
	eventSink     func(ChatEvent)
}

func NewApp() *App {
	benchmarks := newModelBenchmarkStore("")
	return &App{
		cancels:       make(map[string]context.CancelFunc),
		conversations: newConversationStore(""),
		profiles:      newConnectionProfileStore(""),
		benchmarks:    benchmarks,
		syncV2:        newBenchmarkSyncV2Store(""),
	}
}

func (a *App) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	a.ctx = ctx
	// A broken optional sync listener must not stop the chat application from
	// starting. The v2 store records a sanitized failure in its local audit log.
	_ = a.syncV2.StartConfiguredEndpoint()
	return nil
}

func (a *App) ServiceShutdown() error {
	a.mu.Lock()
	for id, cancel := range a.cancels {
		cancel()
		delete(a.cancels, id)
	}
	a.mu.Unlock()
	return a.syncV2.Close()
}

// GetBenchmarkSyncV2Identity creates the local v2 identity on first use and
// returns only its public information. Pairing uses these fingerprints to help
// a user spot a different or replaced device key.
func (a *App) GetBenchmarkSyncV2Identity() (BenchmarkSyncV2Identity, error) {
	return a.syncV2.Identity()
}

// PrepareModelBenchmarkForSecureSync adds a durable v2 proof to an already
// completed local result. The forthcoming v2 transport calls this before it
// creates an encrypted outbound record batch.
func (a *App) PrepareModelBenchmarkForSecureSync(id string) (ModelBenchmark, error) {
	benchmark, err := a.benchmarks.Open(id)
	if err != nil {
		return ModelBenchmark{}, err
	}
	signed, err := a.syncV2.SignRecord(benchmark)
	if err != nil {
		return ModelBenchmark{}, err
	}
	return a.benchmarks.SaveV2Proof(signed)
}

func (a *App) ListModels(profile ConnectionProfile) ([]Model, error) {
	client, err := openai.NewClient(profile.BaseURL, profile.APIKey, nil)
	if err != nil {
		return nil, err
	}

	// Hosted providers can return several hundred models. Allow enough time for
	// an initial catalog request instead of treating a slow remote list as a
	// failed connection.
	ctx, cancel := context.WithTimeout(a.applicationContext(), 45*time.Second)
	defer cancel()

	models, err := client.ListModels(ctx)
	if err != nil {
		return nil, friendlyError(err)
	}

	result := make([]Model, 0, len(models))
	for _, model := range models {
		result = append(result, Model{ID: model.ID, OwnedBy: model.OwnedBy})
	}
	return result, nil
}

func (a *App) ListConversations() ([]ConversationSummary, error) {
	return a.conversations.List()
}

func (a *App) OpenConversation(id string) (Conversation, error) {
	return a.conversations.Open(id)
}

func (a *App) DeleteConversation(id string) error {
	return a.conversations.Delete(id)
}

func (a *App) CreateConversation() (Conversation, error) {
	return a.conversations.Create()
}

func (a *App) SaveConversation(conversation Conversation) (Conversation, error) {
	return a.conversations.Save(conversation)
}

func (a *App) LoadConnectionProfile() (SavedConnectionProfile, error) {
	return a.profiles.Load()
}

func (a *App) SaveConnectionProfile(profile SavedConnectionProfile) (SavedConnectionProfile, error) {
	return a.profiles.Save(profile)
}

func (a *App) ListSavedConnectionProfiles() ([]SavedConnectionProfile, error) {
	return a.profiles.List()
}

func (a *App) SaveNamedConnectionProfile(profile SavedConnectionProfile) (SavedConnectionProfile, error) {
	return a.profiles.SaveNamed(profile)
}

func (a *App) DeleteSavedConnectionProfile(id string) error {
	return a.profiles.DeleteNamed(id)
}

func (a *App) CreateModelBenchmark(benchmark ModelBenchmark) (ModelBenchmark, error) {
	return a.benchmarks.Create(benchmark)
}

func (a *App) SaveModelBenchmark(benchmark ModelBenchmark) (ModelBenchmark, error) {
	return a.benchmarks.Save(benchmark)
}

func (a *App) ListModelBenchmarks() ([]ModelBenchmarkSummary, error) {
	return a.benchmarks.List()
}

func (a *App) OpenModelBenchmark(id string) (ModelBenchmark, error) {
	return a.benchmarks.Open(id)
}

func (a *App) DeleteModelBenchmark(id string) error {
	return a.benchmarks.Delete(id)
}

// ImportBenchmarkReport adds new results embedded in an exported Markdown or
// HTML benchmark report to the local benchmark history.
func (a *App) ImportBenchmarkReport(path string) (ModelBenchmarkImportResult, error) {
	return a.benchmarks.ImportReport(path)
}

func (a *App) GetBenchmarkSyncV2State() (BenchmarkSyncV2State, error) {
	return a.syncV2.State()
}

func (a *App) UpdateBenchmarkSyncV2DeviceName(name string) (BenchmarkSyncV2State, error) {
	return a.syncV2.UpdateDeviceName(name)
}

// ConfigureBenchmarkSyncV2DirectTLS configures and starts the application's
// own v2 HTTPS listener. The private-key contents remain in the selected PEM
// file and are never returned to the frontend or written to app state.
func (a *App) ConfigureBenchmarkSyncV2DirectTLS(publicHTTPSURL, certificatePath, privateKeyPath, listenAddress string) (BenchmarkSyncV2State, error) {
	return a.syncV2.ConfigureDirectTLS(publicHTTPSURL, certificatePath, privateKeyPath, listenAddress)
}

// GenerateBenchmarkSyncV2TLSCertificateRequest makes a new TLS private key
// and certificate signing request in user-selected files. It does not issue a
// trusted certificate; the CSR must be signed by a public or organisation CA.
func (a *App) GenerateBenchmarkSyncV2TLSCertificateRequest(publicHTTPSURL, privateKeyPath, certificateRequestPath string) (BenchmarkSyncV2TLSCertificateRequest, error) {
	return GenerateBenchmarkSyncV2TLSCertificateRequest(publicHTTPSURL, privateKeyPath, certificateRequestPath)
}

func (a *App) StopBenchmarkSyncV2Endpoint() (BenchmarkSyncV2State, error) {
	return a.syncV2.StopEndpoint()
}

// CreateBenchmarkSyncV2PairingInvitation returns a short-lived invitation
// secret exactly once. The secret is never written to the sync state or logs.
func (a *App) CreateBenchmarkSyncV2PairingInvitation() (BenchmarkSyncV2Invitation, error) {
	return a.syncV2.CreatePairingInvitation()
}

func (a *App) StartBenchmarkSyncV2Pairing(publicHTTPSURL, pairingSecret string) (BenchmarkSyncV2OutgoingPairing, error) {
	return a.syncV2.StartPairing(publicHTTPSURL, pairingSecret)
}

func (a *App) CheckBenchmarkSyncV2Pairing(requestID string) (BenchmarkSyncV2OutgoingPairing, error) {
	return a.syncV2.CheckPairing(requestID)
}

func (a *App) ConfirmBenchmarkSyncV2Pairing(requestID string) (BenchmarkSyncV2State, error) {
	return a.syncV2.ConfirmPairing(requestID)
}

func (a *App) ApproveBenchmarkSyncV2Pairing(requestID string) (BenchmarkSyncV2State, error) {
	return a.syncV2.ApprovePairing(requestID)
}

func (a *App) RejectBenchmarkSyncV2Pairing(requestID string) (BenchmarkSyncV2State, error) {
	return a.syncV2.RejectPairing(requestID)
}

func (a *App) ClearBenchmarkSyncV2Logs() (BenchmarkSyncV2State, error) {
	return a.syncV2.ClearLogs()
}

// SaveBenchmarkExport writes a user-selected benchmark report.
func (a *App) SaveBenchmarkExport(path string, contents string) error {
	return saveTextExport(path, contents)
}

// SaveChatShare writes a user-selected chat response share file.
func (a *App) SaveChatShare(path string, contents string) error {
	return saveTextExport(path, contents)
}

// saveTextExport only permits the two formats offered by the app. Keeping the
// check in the backend prevents the frontend service from becoming an
// arbitrary file writer.
func saveTextExport(path string, contents string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("저장할 파일을 선택해 주세요")
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".md":
	default:
		return errors.New("HTML 또는 Markdown 파일로만 저장할 수 있습니다")
	}

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("파일을 저장할 수 없습니다: %w", err)
	}
	return nil
}

func (a *App) StartChat(request ChatRequest) error {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Model = strings.TrimSpace(request.Model)
	reasoningEffort, err := normalizeReasoningEffort(request.ReasoningEffort)
	if err != nil {
		return err
	}
	if request.RequestID == "" {
		return errors.New("요청 ID가 없습니다")
	}
	if request.Model == "" {
		return errors.New("사용할 모델을 선택해 주세요")
	}
	if len(request.Messages) == 0 {
		return errors.New("전송할 메시지가 없습니다")
	}

	// A streaming response has its own activity-based watchdog. Do not let the
	// standard five-minute HTTP timeout stop a response that is still producing
	// useful text.
	client, err := openai.NewClient(request.Profile.BaseURL, request.Profile.APIKey, streamingHTTPClient())
	if err != nil {
		return err
	}

	messages := make([]openai.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		messages = append(messages, openai.Message{Role: message.Role, Content: message.Content})
	}
	if len(messages) == 0 {
		return errors.New("전송할 메시지가 없습니다")
	}

	ctx, cancel := context.WithCancel(a.applicationContext())
	if err := a.storeCancel(request.RequestID, cancel); err != nil {
		cancel()
		return err
	}

	go a.runChat(ctx, request.RequestID, client, request.Model, messages, reasoningEffort, request.Benchmark)
	return nil
}

func (a *App) CancelChat(requestID string) bool {
	a.mu.Lock()
	cancel, ok := a.cancels[requestID]
	a.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (a *App) runChat(
	ctx context.Context,
	requestID string,
	client *openai.Client,
	model string,
	messages []openai.Message,
	reasoningEffort string,
	benchmark bool,
) {
	defer a.removeCancel(requestID)
	startedAt := time.Now()
	var firstTokenAt time.Time
	policy := defaultChatStreamTimeoutPolicy
	if benchmark {
		policy = defaultBenchmarkStreamTimeoutPolicy
	}
	watchdog := newStreamWatchdog(startedAt, func() { a.CancelChat(requestID) }, policy)
	go watchdog.watch(ctx)
	defer watchdog.stop()
	a.emit(ChatEvent{RequestID: requestID, Type: "started"})

	err := client.StreamChat(ctx, openai.ChatRequest{Model: model, Messages: messages, ReasoningEffort: reasoningEffort}, func(chunk openai.StreamChunk) {
		if ctx.Err() != nil {
			return
		}
		if chunk.Delta != "" {
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
			}
			watchdog.recordOutput()
			a.emit(ChatEvent{RequestID: requestID, Type: "delta", Delta: chunk.Delta})
		}
		if chunk.Usage != nil {
			a.emit(ChatEvent{RequestID: requestID, Type: "usage", Usage: &TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}})
		}
	})
	metrics := responseMetrics(startedAt, firstTokenAt)
	watchdog.stop()
	if timeoutErr := watchdog.timeoutError(); timeoutErr != nil {
		a.emit(ChatEvent{RequestID: requestID, Type: "failed", Metrics: metrics, Error: timeoutErr.Error()})
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		a.emit(ChatEvent{RequestID: requestID, Type: "cancelled", Metrics: metrics})
		return
	}
	if err == nil {
		a.emit(ChatEvent{RequestID: requestID, Type: "completed", Metrics: metrics})
		return
	}
	if errors.Is(err, context.Canceled) {
		a.emit(ChatEvent{RequestID: requestID, Type: "cancelled", Metrics: metrics})
		return
	}
	a.emit(ChatEvent{RequestID: requestID, Type: "failed", Metrics: metrics, Error: friendlyError(err).Error()})
}

func streamingHTTPClient() *http.Client {
	return &http.Client{}
}

func responseMetrics(startedAt, firstTokenAt time.Time) *ResponseMetrics {
	metrics := &ResponseMetrics{TotalDurationMs: time.Since(startedAt).Milliseconds()}
	if !firstTokenAt.IsZero() {
		metrics.FirstTokenDurationMs = firstTokenAt.Sub(startedAt).Milliseconds()
	}
	return metrics
}

func (a *App) storeCancel(requestID string, cancel context.CancelFunc) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.cancels[requestID]; exists {
		return errors.New("같은 요청이 이미 실행 중입니다")
	}
	a.cancels[requestID] = cancel
	return nil
}

func (a *App) removeCancel(requestID string) {
	a.mu.Lock()
	delete(a.cancels, requestID)
	a.mu.Unlock()
}

func (a *App) emit(event ChatEvent) {
	if a.eventSink != nil {
		a.eventSink(event)
		return
	}
	if application.Get() != nil {
		application.Get().Event.Emit(chatEventName, event)
	}
}

func (a *App) applicationContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func friendlyError(err error) error {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401, 403:
			return errors.New("인증에 실패했습니다. API 키를 확인해 주세요")
		case 404:
			return errors.New("API 경로나 모델을 찾을 수 없습니다")
		case 429:
			return errors.New("요청 한도를 초과했습니다. 잠시 후 다시 시도해 주세요")
		}
		if apiErr.Message != "" {
			return fmt.Errorf("AI 서버 오류: %s", apiErr.Message)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("AI 서버 응답 시간이 초과되었습니다")
	}
	return err
}
