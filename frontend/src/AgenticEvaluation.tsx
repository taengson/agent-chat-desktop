import {useCallback, useEffect, useMemo, useState} from 'react';
import {Events} from '@wailsio/runtime';
import {App as ChatService} from '../bindings/github.com/taengson/agent-chat-desktop';
import type {
    AgenticEvaluation,
    AgenticEvaluationAction,
    AgenticEvaluationRun,
    AgenticEvaluationScenarioSummary,
    AgenticEvaluationSummary,
    ConnectionProfile,
    Model,
    SavedConnectionProfile,
} from '../bindings/github.com/taengson/agent-chat-desktop/models';
import OpenRouterModelPicker, {isOpenRouterURL} from './OpenRouterModelPicker';
import {
    reasoningEffortLabel,
    reasoningEffortOptions,
    reasoningEffortWarning,
    type ReasoningEffort,
} from './reasoningEffort';

const agenticEvaluationEventName = 'agentic-evaluation:event';
const maxAgenticEvaluationRuns = 240;

interface AgenticEvaluationWorkspaceProps {
    profiles: SavedConnectionProfile[];
    connectionAPIKey: string;
    openRouterModelIDs: string[];
    onOpenRouterModelIDsChange: (modelIDs: string[]) => void;
    onBusyChange: (busy: boolean) => void;
}

type ModelAggregate = {
    model: string;
    total: number;
    finished: number;
    passed: number;
    actions: number;
    inputTokens: number;
    outputTokens: number;
};

type EnvironmentAggregate = {
    environment: string;
    total: number;
    finished: number;
    passed: number;
};

function formatTime(value?: string): string {
    if (!value) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat('ko-KR', {
        month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    }).format(date);
}

function formatDuration(milliseconds?: number): string {
    if (milliseconds === undefined || milliseconds <= 0) return '—';
    const seconds = milliseconds / 1_000;
    return `${new Intl.NumberFormat('ko-KR', {maximumFractionDigits: seconds < 10 ? 1 : 0}).format(seconds)}초`;
}

function runStatusText(status: string): string {
    const labels: Record<string, string> = {
        pending: '대기', running: '실행 중', success: '성공', failed: '목표 미달',
        cancelled: '취소됨', interrupted: '중단됨', action_limit: '행동 한도',
        context_limit: '문맥 한도', time_limit: '시간 한도', connection_error: '연결 오류',
    };
    return labels[status] || status || '알 수 없음';
}

function evaluationStatusText(status: string): string {
    if (status === 'running') return '실행 중';
    if (status === 'completed') return '완료';
    if (status === 'cancelled') return '취소됨';
    return status || '알 수 없음';
}

function actionLabel(action: AgenticEvaluationAction): string {
    if (action.type === 'tool') return action.toolName || '도구 호출';
    if (action.type === 'complete') return '완료 선언';
    return '형식 오류';
}

function aggregateByModel(runs: AgenticEvaluationRun[]): ModelAggregate[] {
    const aggregate = new Map<string, ModelAggregate>();
    for (const run of runs) {
        const current = aggregate.get(run.model) || {
            model: run.model, total: 0, finished: 0, passed: 0, actions: 0, inputTokens: 0, outputTokens: 0,
        };
        current.total += 1;
        current.actions += (run.actions || []).length;
        current.inputTokens += run.usage?.promptTokens || 0;
        current.outputTokens += run.usage?.completionTokens || 0;
        if (run.status !== 'pending' && run.status !== 'running') current.finished += 1;
        if (run.result?.passed) current.passed += 1;
        aggregate.set(run.model, current);
    }
    return Array.from(aggregate.values()).sort((left, right) => left.model.localeCompare(right.model));
}

function aggregateByEnvironment(runs: AgenticEvaluationRun[]): EnvironmentAggregate[] {
    const aggregate = new Map<string, EnvironmentAggregate>();
    for (const run of runs) {
        const current = aggregate.get(run.environment) || {environment: run.environment, total: 0, finished: 0, passed: 0};
        current.total += 1;
        if (run.status !== 'pending' && run.status !== 'running') current.finished += 1;
        if (run.result?.passed) current.passed += 1;
        aggregate.set(run.environment, current);
    }
    return Array.from(aggregate.values()).sort((left, right) => left.environment.localeCompare(right.environment));
}

function groupScenarios(scenarios: AgenticEvaluationScenarioSummary[]) {
    return scenarios.reduce<Record<string, AgenticEvaluationScenarioSummary[]>>((groups, scenario) => {
        groups[scenario.environment] = [...(groups[scenario.environment] || []), scenario];
        return groups;
    }, {});
}

export default function AgenticEvaluationWorkspace({
    profiles,
    connectionAPIKey,
    openRouterModelIDs,
    onOpenRouterModelIDsChange,
    onBusyChange,
}: AgenticEvaluationWorkspaceProps) {
    const [profileID, setProfileID] = useState('');
    const [apiKey, setAPIKey] = useState('');
    const [models, setModels] = useState<Model[]>([]);
    const [selectedModels, setSelectedModels] = useState<string[]>([]);
    const [loadingModels, setLoadingModels] = useState(false);
    const [openRouterPickerOpen, setOpenRouterPickerOpen] = useState(false);
    const [scenarios, setScenarios] = useState<AgenticEvaluationScenarioSummary[]>([]);
    const [selectedScenarioIDs, setSelectedScenarioIDs] = useState<string[]>([]);
    const [repetitions, setRepetitions] = useState(1);
    const [reasoningEffort, setReasoningEffort] = useState<ReasoningEffort>('');
    const [history, setHistory] = useState<AgenticEvaluationSummary[]>([]);
    const [loadingHistory, setLoadingHistory] = useState(true);
    const [evaluation, setEvaluation] = useState<AgenticEvaluation | null>(null);
    const [view, setView] = useState<'home' | 'result'>('home');
    const [error, setError] = useState('');
    const [starting, setStarting] = useState(false);
    const [cancelling, setCancelling] = useState(false);

    const selectedProfile = useMemo(
        () => profiles.find((profile) => profile.id === profileID),
        [profileID, profiles],
    );
    const usingOpenRouter = isOpenRouterURL(selectedProfile?.baseURL || '');
    const availableModels = useMemo(() => (
        usingOpenRouter
            ? models.filter((model) => openRouterModelIDs.includes(model.id))
            : models
    ), [models, openRouterModelIDs, usingOpenRouter]);
    const scenarioGroups = useMemo(() => groupScenarios(scenarios), [scenarios]);
    const running = evaluation?.status === 'running';
    const reasoningWarning = reasoningEffortWarning(reasoningEffort);
    const plannedRunCount = selectedModels.length * selectedScenarioIDs.length * repetitions;
    const runCountOverLimit = plannedRunCount > maxAgenticEvaluationRuns;

    const refreshHistory = useCallback(async () => {
        try {
            const loaded = await ChatService.ListAgenticEvaluations();
            setHistory(loaded || []);
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setLoadingHistory(false);
        }
    }, []);

    const refreshEvaluation = useCallback(async (id: string) => {
        try {
            const opened = await ChatService.OpenAgenticEvaluation(id);
            setEvaluation(opened);
            return opened;
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
            return null;
        }
    }, []);

    useEffect(() => {
        if (connectionAPIKey) setAPIKey(connectionAPIKey);
    }, [connectionAPIKey]);

    useEffect(() => {
        if (!profileID && profiles.length > 0) setProfileID(profiles[0].id);
        if (profileID && !profiles.some((profile) => profile.id === profileID)) setProfileID(profiles[0]?.id || '');
    }, [profileID, profiles]);

    useEffect(() => {
        let active = true;
        void ChatService.ListAgenticEvaluationScenarios().then((loaded) => {
            if (!active) return;
            const next = loaded || [];
            setScenarios(next);
            setSelectedScenarioIDs((current) => current.length > 0
                ? current.filter((id) => next.some((scenario) => scenario.id === id))
                : next.map((scenario) => scenario.id));
        }).catch((reason) => {
            if (active) setError(reason instanceof Error ? reason.message : String(reason));
        });
        return () => { active = false; };
    }, []);

    useEffect(() => {
        setLoadingHistory(true);
        void refreshHistory();
    }, [refreshHistory]);

    useEffect(() => {
        onBusyChange(running);
        return () => onBusyChange(false);
    }, [onBusyChange, running]);

    useEffect(() => {
        const listener = Events.On(agenticEvaluationEventName, (event) => {
            const payload = event.data as {evaluationID?: string};
            if (!payload.evaluationID) return;
            void refreshHistory();
            if (evaluation?.id === payload.evaluationID) void refreshEvaluation(payload.evaluationID);
        });
        return listener;
    }, [evaluation?.id, refreshEvaluation, refreshHistory]);

    useEffect(() => {
        setSelectedModels((current) => current.filter((id) => availableModels.some((model) => model.id === id)));
    }, [availableModels]);

    async function loadModels() {
        if (!selectedProfile) {
            setError('연결 프로필을 선택해 주세요.');
            return;
        }
        try {
            setLoadingModels(true);
            setError('');
            const loaded = await ChatService.ListModels({baseURL: selectedProfile.baseURL, apiKey});
            const next = loaded || [];
            setModels(next);
            if (isOpenRouterURL(selectedProfile.baseURL)) {
                setSelectedModels((current) => current.filter((id) => openRouterModelIDs.includes(id)));
            } else {
                setSelectedModels((current) => current.length > 0
                    ? current.filter((id) => next.some((model) => model.id === id))
                    : next.slice(0, 1).map((model) => model.id));
            }
        } catch (reason) {
            setModels([]);
            setSelectedModels([]);
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setLoadingModels(false);
        }
    }

    function toggleModel(modelID: string) {
        setSelectedModels((current) => current.includes(modelID)
            ? current.filter((id) => id !== modelID)
            : [...current, modelID]);
    }

    function toggleScenario(scenarioID: string) {
        setSelectedScenarioIDs((current) => current.includes(scenarioID)
            ? current.filter((id) => id !== scenarioID)
            : [...current, scenarioID]);
    }

    function setEnvironmentSelection(environment: string, selected: boolean) {
        const ids = (scenarioGroups[environment] || []).map((scenario) => scenario.id);
        setSelectedScenarioIDs((current) => selected
            ? Array.from(new Set([...current, ...ids]))
            : current.filter((id) => !ids.includes(id)));
    }

    async function startEvaluation() {
        if (!selectedProfile || !apiKey.trim() || selectedModels.length === 0 || selectedScenarioIDs.length === 0) {
            setError('연결 프로필, API 키, 모델, 시나리오를 모두 선택해 주세요.');
            return;
        }
        try {
            setStarting(true);
            setError('');
            const profile: ConnectionProfile = {baseURL: selectedProfile.baseURL, apiKey: apiKey.trim()};
            const started = await ChatService.StartAgenticEvaluation({
                profile,
                profileID: selectedProfile.id,
                profileName: selectedProfile.name,
                modelIDs: selectedModels,
                scenarioIDs: selectedScenarioIDs,
                repetitions,
                reasoningEffort,
            });
            setEvaluation(started);
            setView('result');
            void refreshHistory();
            void refreshEvaluation(started.id);
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setStarting(false);
        }
    }

    async function cancelEvaluation() {
        if (!evaluation || !running) return;
        try {
            setCancelling(true);
            setError('');
            await ChatService.CancelAgenticEvaluation(evaluation.id);
            await refreshEvaluation(evaluation.id);
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setCancelling(false);
        }
    }

    async function openEvaluation(id: string) {
        const opened = await refreshEvaluation(id);
        if (opened) setView('result');
    }

    async function deleteEvaluation(id: string) {
        if (running || !window.confirm('이 실행 기록을 삭제할까요?')) return;
        try {
            setError('');
            await ChatService.DeleteAgenticEvaluation(id);
            if (evaluation?.id === id) {
                setEvaluation(null);
                setView('home');
            }
            await refreshHistory();
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        }
    }

    function applyOpenRouterModels(nextIDs: string[]) {
        onOpenRouterModelIDsChange(nextIDs);
        setSelectedModels((current) => current.filter((id) => nextIDs.includes(id)));
    }

    function renderRun(run: AgenticEvaluationRun) {
        const result = run.result;
        return <article className="agentic-run-card" key={run.id}>
            <header className="agentic-run-heading">
                <div>
                    <span>{run.environment} · {run.category}</span>
                    <strong>{run.title}</strong>
                    <small>{run.model} · 변형 {run.variant} · {run.scenarioID}@{run.scenarioVersion}</small>
                    {run.initialStateHash && <small>초기 상태 {run.initialStateHash.slice(0, 12)} · 채점 {run.graderVersion}</small>}
                </div>
                <span className={`agentic-status ${run.status}`}>{runStatusText(run.status)}</span>
            </header>
            <p className="agentic-run-goal">{run.goal}</p>
            {result && <section className={`agentic-result ${result.passed ? 'passed' : 'failed'}`}>
                <strong>{result.passed ? '상태 평가 통과' : '상태 평가 미달'}</strong>
                <p>{result.summary}</p>
                {(result.requirements || []).length > 0 && <ul><li>확인 조건: {(result.requirements || []).join(' · ')}</li></ul>}
                {(result.violations || []).length > 0 && <ul className="agentic-violations">{(result.violations || []).map((violation) => <li key={violation}>{violation}</li>)}</ul>}
            </section>}
            {run.error && <p className="agentic-run-error">{run.error}</p>}
            <div className="agentic-run-metrics">
                <span>행동 {(run.actions || []).length}회</span>
                {run.metrics && <span>응답 {formatDuration(run.metrics.totalDurationMs)}</span>}
                {run.usage && <span>입력 {run.usage.promptTokens} · 출력 {run.usage.completionTokens} 토큰</span>}
                {run.finishedAt && <span>{formatTime(run.finishedAt)}</span>}
            </div>
            {(run.actions || []).length > 0 && <details className="agentic-details">
                <summary>행동 기록 {(run.actions || []).length}개</summary>
                <ol className="agentic-action-list">
                    {(run.actions || []).map((action) => <li key={`${action.step}-${action.occurredAt}`} className={action.status}>
                        <div><strong>{action.step}. {actionLabel(action)}</strong><span>{action.status === 'success' ? '성공' : '오류'}</span></div>
                        {action.arguments && <code>{action.arguments}</code>}
                        {action.output && <pre>{action.output}</pre>}
                        {action.rawContent && action.type === 'invalid' && <pre>{action.rawContent}</pre>}
                    </li>)}
                </ol>
            </details>}
            {(run.stateChanges || []).length > 0 && <details className="agentic-details">
                <summary>상태 변경 {(run.stateChanges || []).length}개</summary>
                <div className="agentic-change-list">
                    {(run.stateChanges || []).map((change) => <article key={`${change.resource}-${change.after}`}>
                        <strong>{change.resource}</strong>
                        {change.before && <pre>{change.before}</pre>}
                        <span>→</span>
                        {change.after && <pre>{change.after}</pre>}
                    </article>)}
                </div>
            </details>}
        </article>;
    }

    if (view === 'result' && evaluation) {
        const runs = evaluation.runs || [];
        const aggregate = aggregateByModel(runs);
        const environmentAggregate = aggregateByEnvironment(runs);
        const finishedRuns = runs.filter((run) => run.status !== 'pending' && run.status !== 'running').length;
        const passedRuns = runs.filter((run) => run.result?.passed).length;
        return <section className="agentic-page" aria-label="에이전트 실험 결과">
            <header className="agentic-header">
                <div>
                    <span className="eyebrow">AGENTIC EVALUATION</span>
                    <h1>{running ? '에이전트 실험 실행 중' : '에이전트 실험 결과'}</h1>
                    <p>{evaluation.profileName} · 추론 {reasoningEffortLabel(evaluation.reasoningEffort || '')} · {formatTime(evaluation.createdAt)}</p>
                </div>
                <div className="agentic-header-actions">
                    <button className="text-button" type="button" disabled={running} onClick={() => setView('home')}>실험 홈</button>
                    {running && <button className="danger-button" type="button" onClick={() => void cancelEvaluation()} disabled={cancelling}>{cancelling ? '취소 중…' : '실행 취소'}</button>}
                </div>
            </header>
            {error && <p className="error-banner">{error}</p>}
            <section className="agentic-summary-grid">
                <div><span>진행</span><strong>{finishedRuns}/{runs.length}</strong></div>
                <div><span>상태 평가 통과</span><strong>{passedRuns}/{runs.length}</strong></div>
                <div><span>선택 모델</span><strong>{evaluation.modelIDs?.length || 0}</strong></div>
                <div><span>반복</span><strong>{evaluation.repetitions}회</strong></div>
            </section>
            <section className="agentic-info-card">
                <strong>실행 격리</strong>
                <p>각 실행은 시나리오 원본에서 새 상태를 만들고, 허용된 가상 도구만 호출합니다. 실행이 끝나면 원본과 작업 사본은 폐기되고 행동 기록과 상태 차이만 보관됩니다.</p>
                <div className="agentic-rule-list">
                    <span>행동 {evaluation.executionRules.actionFormatVersion}</span>
                    <span>지시 {evaluation.executionRules.systemPromptVersion}</span>
                    <span>도구 {evaluation.executionRules.toolDefinitionVersion}</span>
                    <span>채점 {evaluation.executionRules.graderVersion}</span>
                    <span>최대 {evaluation.executionRules.maxActions} 행동 · {Math.floor(evaluation.executionRules.runTimeoutSeconds / 60)}분</span>
                </div>
            </section>
            <section className="agentic-comparison-card">
                <div className="agentic-card-heading"><div><span className="eyebrow">MODEL COMPARISON</span><h2>모델별 실행 요약</h2></div><small>같은 시나리오 변형을 각 모델에 순차 실행</small></div>
                <div className="agentic-comparison-table" role="table" aria-label="모델별 에이전트 실험 결과">
                    <div className="agentic-comparison-row header" role="row"><span>모델</span><span>통과</span><span>완료</span><span>평균 행동</span><span>토큰</span></div>
                    {aggregate.map((item) => <div className="agentic-comparison-row" role="row" key={item.model}>
                        <strong title={item.model}>{item.model}</strong><span>{item.passed}/{item.total}</span><span>{item.finished}/{item.total}</span><span>{item.total ? (item.actions / item.total).toFixed(1) : '—'}</span><span>{item.inputTokens + item.outputTokens}</span>
                    </div>)}
                </div>
                <div className="agentic-environment-summary" aria-label="환경별 성공 현황">
                    {environmentAggregate.map((item) => <article key={item.environment}><span>{item.environment}</span><strong>{item.passed}/{item.total}</strong><small>{item.finished}/{item.total} 완료</small></article>)}
                </div>
            </section>
            <section className="agentic-runs-section">
                <div className="agentic-card-heading"><div><span className="eyebrow">RUN TRACE</span><h2>실행별 결과</h2></div><small>도구 호출, 복구 시도, 최종 상태 평가를 확인할 수 있습니다.</small></div>
                <div className="agentic-run-list">{runs.map(renderRun)}</div>
            </section>
        </section>;
    }

    return <section className="agentic-page" aria-label="에이전트 실험">
        <header className="agentic-header">
            <div>
                <span className="eyebrow">AGENTIC EVALUATION</span>
                <h1>에이전트 실험</h1>
                <p>모델이 제한된 가상 환경에서 도구를 고르고, 오류를 복구하며, 목표 상태를 만드는 과정을 평가합니다.</p>
            </div>
        </header>
        {error && <p className="error-banner">{error}</p>}
        <section className="agentic-info-card">
            <strong>안전한 가상 실행</strong>
            <p>실제 파일, 셸, 네트워크에는 접근하지 않습니다. 문서 작업 공간과 업무 기록 도구의 사본을 메모리에서 제공하고 실행 후 버립니다.</p>
        </section>
        <section className="agentic-setup-grid">
            <article className="agentic-setup-card">
                <div className="agentic-card-heading"><div><span className="eyebrow">1. CONNECTION</span><h2>연결과 모델</h2></div></div>
                <label className="agentic-field"><span>연결 프로필</span><select value={profileID} onChange={(event) => { setProfileID(event.target.value); setModels([]); setSelectedModels([]); }}>
                    <option value="">선택하세요</option>{profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name}</option>)}
                </select></label>
                <label className="agentic-field"><span>API 키</span><input value={apiKey} onChange={(event) => setAPIKey(event.target.value)} type="password" placeholder="실행 중에만 사용합니다" autoComplete="off" /></label>
                <div className="agentic-model-load"><button className="secondary-button" type="button" onClick={() => void loadModels()} disabled={loadingModels || !selectedProfile}>{loadingModels ? '모델 불러오는 중…' : '모델 불러오기'}</button>
                    {usingOpenRouter && <button className="text-button" type="button" onClick={() => setOpenRouterPickerOpen(true)} disabled={models.length === 0}>표시 모델 고르기</button>}</div>
                <div className="agentic-model-selection" aria-label="실험할 모델">
                    {availableModels.length === 0 ? <p>모델을 불러온 뒤 하나 이상 선택하세요.</p> : availableModels.map((model) => <label key={model.id}><input type="checkbox" checked={selectedModels.includes(model.id)} onChange={() => toggleModel(model.id)} /><span>{model.id}</span></label>)}
                </div>
            </article>
            <article className="agentic-setup-card">
                <div className="agentic-card-heading"><div><span className="eyebrow">2. SCENARIOS</span><h2>가상 환경과 시나리오</h2></div><small>{selectedScenarioIDs.length}/{scenarios.length}개 선택</small></div>
                <div className="agentic-scenario-groups">{Object.entries(scenarioGroups).map(([environment, items]) => {
                    const allSelected = items.every((scenario) => selectedScenarioIDs.includes(scenario.id));
                    return <section key={environment}><label className="agentic-environment"><input type="checkbox" checked={allSelected} onChange={(event) => setEnvironmentSelection(environment, event.target.checked)} /><strong>{environment}</strong></label>
                        {items.map((scenario) => <label className="agentic-scenario" key={scenario.id}><input type="checkbox" checked={selectedScenarioIDs.includes(scenario.id)} onChange={() => toggleScenario(scenario.id)} /><span><strong>{scenario.title}</strong><small>{scenario.category} · {scenario.description}</small></span></label>)}
                    </section>;
                })}</div>
            </article>
            <article className="agentic-setup-card agentic-run-settings">
                <div className="agentic-card-heading"><div><span className="eyebrow">3. RUN</span><h2>반복 조건</h2></div></div>
                <label className="agentic-field"><span>시나리오당 반복</span><select value={repetitions} onChange={(event) => setRepetitions(Number(event.target.value))}>{[1, 2, 3, 4, 5].map((count) => <option value={count} key={count}>{count}회</option>)}</select></label>
                <label className="agentic-field"><span>추론 강도</span><select value={reasoningEffort} onChange={(event) => setReasoningEffort(event.target.value as ReasoningEffort)}>{reasoningEffortOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label>
                {reasoningWarning && <p className="agentic-warning">{reasoningWarning}</p>}
                <p className="agentic-run-count">총 <strong>{plannedRunCount}</strong>회가 하나의 순차 대기열에서 실행됩니다.</p>
                <button className="primary-button" type="button" onClick={() => void startEvaluation()} disabled={starting || runCountOverLimit || !selectedProfile || selectedModels.length === 0 || selectedScenarioIDs.length === 0}>{starting ? '실험 준비 중…' : '에이전트 실험 시작'}</button>
                {runCountOverLimit && <p className="agentic-warning">한 대기열은 최대 {maxAgenticEvaluationRuns}회까지 실행할 수 있습니다. 모델, 시나리오 또는 반복 횟수를 줄여 주세요.</p>}
            </article>
        </section>
        <section className="agentic-history-card">
            <div className="agentic-card-heading"><div><span className="eyebrow">HISTORY</span><h2>저장된 실행</h2></div><small>{loadingHistory ? '불러오는 중…' : `${history.length}/80개`}</small></div>
            {history.length === 0 && !loadingHistory ? <p className="agentic-empty">아직 저장된 에이전트 실험이 없습니다.</p> : <div className="agentic-history-list">{history.map((record) => <article key={record.id}>
                <button type="button" onClick={() => void openEvaluation(record.id)}><strong>{record.models?.join(', ') || '모델 없음'}</strong><span>{record.profileName} · 시나리오 {record.scenarioCount}개 · 반복 {record.repetitions}회</span><small>{evaluationStatusText(record.status)} · 통과 {record.passedRunCount}/{record.runCount} · {formatTime(record.updatedAt)}</small></button>
                <button className="text-button danger" type="button" onClick={() => void deleteEvaluation(record.id)} disabled={record.status === 'running'}>삭제</button>
            </article>)}</div>}
        </section>
        <OpenRouterModelPicker
            open={openRouterPickerOpen}
            models={models}
            selectedModel=""
            selectedModelIDs={openRouterModelIDs}
            onClose={() => setOpenRouterPickerOpen(false)}
            onApply={applyOpenRouterModels}
        />
    </section>;
}
