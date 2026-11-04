import {useEffect, useMemo, useRef, useState, type ReactNode} from 'react';
import {Dialogs, Events, System} from '@wailsio/runtime';
import {App as ChatService} from '../bindings/github.com/taengson/agent-chat-desktop';
import {
    createDecisionDraft, createDecisionSuite, decisionDraftStorageKey, decisionTypeInfo, decisionTypes,
    neutralDecisionText, parseDecisionCSV, parseDecisionSuite, restoreDecisionDraft, selectedDecisionCases, validateDecisionSuite, validateDecisionTargets,
    type DecisionCase, type DecisionDraft, type DecisionLanguage, type DecisionLanguageSelection,
    type DecisionSuite, type DecisionTarget, type DecisionType,
} from './decisionBenchmarkDraft';
import './DecisionBenchmark.css';
import type * as DecisionModels from '../bindings/github.com/taengson/agent-chat-desktop/models';
import DecisionBenchmarkResults from './DecisionBenchmarkResults';

export type DecisionView = 'setup' | 'dataset' | 'results';
export interface DecisionSidebarState {
    targetCount: number;
    caseCount: number;
    language: string;
}

const evaluationTypes: DecisionType[] = ['choice', 'ranking'];

const languageLabels: Record<DecisionLanguageSelection, string> = {ko: '한국어', en: '영어', both: '한국어 + 영어'};

function Icon({kind}: {kind: 'models' | 'cases' | 'language' | 'choice' | 'ranking' | 'noul' | 'score' | 'arrow' | 'empty'}) {
    const paths: Record<typeof kind, ReactNode> = {
        models: <><rect x="4" y="4" width="16" height="6" rx="2"/><rect x="4" y="14" width="16" height="6" rx="2"/><path d="M8 7h.01M8 17h.01M15 7h2M15 17h2"/></>,
        cases: <><rect x="5" y="3" width="14" height="18" rx="2"/><path d="M9 8h6M9 12h6M9 16h3"/></>,
        language: <><path d="M3 5h12M9 3v2M12 5c0 5-4 9-8 11M6 8c1 3 3 5 6 7M14 21l4-10 4 10M16 17h4"/></>,
        ranking: <><path d="M4 6h2M10 6h10M4 12h2M10 12h10M4 18h2M10 18h10"/></>,
        choice: <><path d="M4 6h2M10 6h10M4 12h2M10 12h10M4 18h2M10 18h10"/><path d="m3 11 2 2 3-4"/></>,
        noul: <><circle cx="12" cy="12" r="9"/><path d="m8 12 3 3 5-6"/></>,
        score: <><path d="M4 20V14h4v6M10 20V9h4v11M16 20V4h4v16M3 20h18"/></>,
        arrow: <><path d="M5 12h14m-5-5 5 5-5 5"/></>,
        empty: <><path d="M4 6h16v14H4zM8 6V3h8v3M8 11h8M8 15h5"/></>,
    };
    return <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[kind]}</svg>;
}

function loadDraft(): {draft: DecisionDraft; warning: string} {
    try {
        const stored = window.localStorage.getItem(decisionDraftStorageKey);
        const draft = stored ? restoreDecisionDraft(stored) : createDecisionDraft();
        if (draft.suite.source === 'builtin') draft.suite = createDecisionSuite();
        draft.types = draft.types.filter(type => evaluationTypes.includes(type) && draft.suite.tasks.some(task=>task.type===type));
        if (!draft.types.length) draft.types = evaluationTypes.filter(type=>draft.suite.tasks.some(task=>task.type===type));
        return {draft, warning: ''};
    } catch {
        return {draft: createDecisionDraft(), warning: '저장된 설정 초안을 읽을 수 없어 예제 구성을 열었습니다. JSON으로 내보낸 세트가 있다면 다시 가져와 주세요.'};
    }
}

function downloadFile(filename: string, contents: string, mime: string) {
    const url = URL.createObjectURL(new Blob([contents], {type: mime}));
    const link = document.createElement('a');
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

async function saveDatasetFile(format: 'json' | 'csv', contents: string): Promise<boolean> {
    const filename = format === 'json' ? 'decision-suite.json' : 'decision-cases.csv';
    if (System.IsMac() || System.IsWindows() || System.IsLinux() || System.IsMobile()) {
        const path = await Dialogs.SaveFile({
            Title: `Decision 테스트셋 ${format.toUpperCase()} 저장`,
            ButtonText: '저장', Filename: filename,
            Filters: [{DisplayName: `${format.toUpperCase()} 파일`, Pattern: `*.${format}`}],
        });
        if (!path) return false;
        await ChatService.SaveDecisionDatasetExport(path, contents);
    } else {
        downloadFile(filename, contents, format === 'json' ? 'application/json;charset=utf-8' : 'text/csv;charset=utf-8');
    }
    return true;
}

function expectedLabel(item: DecisionCase, suite: DecisionSuite) {
    const task = suite.tasks.find((entry) => entry.id === item.taskID);
    if (task?.type === 'noul') return item.expected === 'true' ? '예' : '아니오';
    if (task?.type === 'score') return `등급 ${item.expected}`;
    return item.expected;
}

export default function DecisionBenchmark({view, onViewChange, onSidebarChange}: {
    view: DecisionView;
    onViewChange: (view: DecisionView) => void;
    onSidebarChange: (state: DecisionSidebarState) => void;
}) {
    const [initial] = useState(loadDraft);
    const [draft, setDraft] = useState({...initial.draft, language: 'ko' as const});
    const [timeoutSeconds, setTimeoutSeconds] = useState(60);
    const [activeRun, setActiveRun] = useState<DecisionModels.DecisionBenchmark | null>(null);
    const [starting, setStarting] = useState(false);
    const [checking, setChecking] = useState('');
    const [connectionChecks, setConnectionChecks] = useState<Record<string, {status: 'checking' | 'success' | 'error'; message: string}>>({});
    const [modelCatalogs, setModelCatalogs] = useState<Record<string, DecisionModels.DecisionModelCatalog>>({});
    const [loadingModels, setLoadingModels] = useState<string[]>([]);
    const [manualModels, setManualModels] = useState<Record<string, boolean>>({});
    const [resultsRefresh, setResultsRefresh] = useState(0);
    const [message, setMessage] = useState(initial.warning);
    const [messageKind, setMessageKind] = useState<'error' | 'success'>('error');
    const [saveMessage, setSaveMessage] = useState('설정 초안 자동 저장');
    const editLanguage: DecisionLanguage = 'ko';
    const [activeTaskID, setActiveTaskID] = useState(initial.draft.suite.tasks[0]?.id || '');
    const [activeCaseID, setActiveCaseID] = useState('');
    const [search, setSearch] = useState('');
    const [apiKeys, setAPIKeys] = useState<Record<string, string>>({});
    const [isImporting, setIsImporting] = useState(false);
    const [exporting, setExporting] = useState<'json' | 'csv' | null>(null);
    const fileRef = useRef<HTMLInputElement>(null);
    const targetsRef = useRef(draft.targets); targetsRef.current = draft.targets;
    const keysRef = useRef(apiKeys); keysRef.current = apiKeys;
    const suite = draft.suite;
    const [configurationResult, setConfigurationResult] = useState<{success: boolean; text: string} | null>(null);
    useEffect(() => {setConfigurationResult(null);}, [draft, timeoutSeconds]);
    const editable = suite.source === 'custom';
    const activeTask = suite.tasks.find((task) => task.id === activeTaskID) || suite.tasks[0];
    const selectedTypes = draft.types.filter(type=>evaluationTypes.includes(type));
    const selectedCases = useMemo(() => selectedDecisionCases(suite, draft.types.filter(type=>evaluationTypes.includes(type)), draft.language), [suite, draft.types, draft.language]);
    const selectedTargets = draft.targets.filter((target) => target.enabled);
    const visibleCases = suite.cases.filter((item) => item.taskID === activeTask?.id && item.language === editLanguage
        && `${item.id} ${item.text} ${item.expected}`.toLocaleLowerCase().includes(search.toLocaleLowerCase()));
    const activeCase = visibleCases.find((item) => item.id === activeCaseID) || visibleCases[0];


    useEffect(() => {
        try {
            window.localStorage.setItem(decisionDraftStorageKey, JSON.stringify(draft));
            setSaveMessage('설정 초안 자동 저장');
        } catch {
            setSaveMessage('자동 저장 불가 · JSON 내보내기로 세트를 보관해 주세요');
        }
    }, [draft]);

    useEffect(() => {
        onSidebarChange({targetCount: selectedTargets.length, caseCount: selectedCases.length, language: languageLabels[draft.language]});
    }, [selectedTargets.length, selectedCases.length, draft.language, onSidebarChange]);

    useEffect(() => {
        let disposed = false;
        let timer: ReturnType<typeof setTimeout>;
        async function refresh() {
            try {
                const id = await ChatService.GetActiveDecisionBenchmark();
                if (id) {
                    const run = await ChatService.OpenDecisionBenchmark(id);
                    if (!disposed) setActiveRun((current) => current?.id === run.id && current.status === 'failed' && run.status === 'running' ? current : run);
                } else if (!disposed) {
                    setActiveRun((run) => {
                        if (run?.status === 'running') {
                            void ChatService.OpenDecisionBenchmark(run.id).then((finished) => {
                                if (!disposed) {setActiveRun(finished); setResultsRefresh((n) => n + 1);}
                            }).catch((error) => {if (!disposed) announce(String(error), 'error');});
                        }
                        return run;
                    });
                }
            } catch (error) {if (!disposed) announce(String(error), 'error');}
            if (!disposed) timer = setTimeout(refresh, 1500);
        }
        void refresh();
        return () => {disposed = true; clearTimeout(timer);};
    }, []);

    useEffect(() => Events.On('decision-benchmark:event', (event) => {
        const payload = event.data as DecisionModels.DecisionBenchmarkEvent;
        if (payload.type === 'failed') {
            announce(payload.error || '실행 기록을 저장할 수 없어 중단했습니다.', 'error');
            setActiveRun((run) => run?.id === payload.id ? {...run, status: 'failed', error: payload.error} : run);
        }
        if (['completed', 'partial', 'cancelled', 'failed'].includes(payload.type)) setResultsRefresh((n) => n + 1);
    }), []);

    async function startBenchmark() {
        const issues = [...validateDecisionSuite(suite), ...validateDecisionTargets(draft.targets)];
        if (!selectedCases.length) issues.push('실행할 문항이 없습니다.');
        if (suite.cases.some((item) => item.language !== 'ko')) issues.push('이번 실행은 한국어 세트만 지원합니다.');
        if (issues.length) {announce(issues.join('\n'), 'error'); return;}
        setStarting(true);
        try {
            const run = await ChatService.StartDecisionBenchmark({
                suite, types: selectedTypes, targets: draft.targets.map((target) => ({...target, name: target.model})), tokens: apiKeys, timeoutSeconds, experimentName: draft.experimentName || '',
            });
            setActiveRun(run); setResultsRefresh((n) => n + 1); onViewChange('results');
        } catch (error) {announce(String(error), 'error');}
        finally {setStarting(false);}
    }

    async function loadTargetModels(target: DecisionTarget) {
        const key = apiKeys[target.id] || '';
        setLoadingModels((ids) => [...ids, target.id]);
        try {
            const catalog = await ChatService.GetDecisionModelCatalog(target, key);
            const current = targetsRef.current.find((t) => t.id === target.id);
            if (!current || current.baseURL !== target.baseURL || current.runtime !== target.runtime || (keysRef.current[target.id] || '') !== key) return;
            setModelCatalogs((items) => ({...items, [target.id]: catalog}));
            const models = catalog.models || [];
            setManualModels((items) => ({...items, [target.id]: !catalog.available && target.runtime !== 'ollama'}));
            if (catalog.available && current.model && !models.includes(current.model)) {
                updateTarget(target.id, {model: ''});
                announce('입력된 모델 ID가 서버 목록에 없습니다. 설치된 정확한 모델을 선택해 주세요.', 'error');
            } else announce(catalog.message);
        } catch (error) {
            const current = targetsRef.current.find((t) => t.id === target.id);
            if (current?.baseURL === target.baseURL && current.runtime === target.runtime && (keysRef.current[target.id] || '') === key) announce(String(error), 'error');
        } finally {setLoadingModels((ids) => ids.filter((id) => id !== target.id));}
    }

    async function checkConnection(target: DecisionTarget) {
        const key = apiKeys[target.id] || '';
        const unchanged = () => {const current = targetsRef.current.find((item) => item.id === target.id); return current?.baseURL === target.baseURL && current.runtime === target.runtime && current.model === target.model && (keysRef.current[target.id] || '') === key;};
        setChecking(target.id);
        setMessage('');
        setConnectionChecks((current) => ({...current, [target.id]: {status: 'checking', message: '서버와 모델의 응답을 확인하고 있습니다.'}}));
        try {
            const result = await ChatService.CheckDecisionConnection(target, key, timeoutSeconds);
            if (!unchanged()) return;
            setConnectionChecks((current) => ({...current, [target.id]: result.labelValid
                ? {status: 'success', message: target.model + ' · 기본 API 응답 확인 · ' + result.durationMs + 'ms'}
                : {status: 'error', message: '서버 응답은 받았지만 판단 형식을 확인할 수 없습니다.'}}));
        } catch (error) {
            if (!unchanged()) return;
            const message = String(error).replace(/^RuntimeError:\s*/, '');
            setConnectionChecks((current) => ({...current, [target.id]: {status: 'error', message}}));
        } finally {setChecking('');}
    }

    function announce(text: string, kind: 'error' | 'success' = 'success') {
        setMessage(text);
        setMessageKind(kind);
    }

    function updateSuite(update: (current: DecisionSuite) => DecisionSuite) {
        if (!editable) return;
        setDraft((current) => ({...current, suite: {...update(current.suite), revision: 'custom-local', reviewStatus: 'unreviewed'}}));
        setMessage('');
    }

    function updateTarget(id: string, changes: Partial<DecisionTarget>) {
        if (changes.baseURL !== undefined || changes.runtime !== undefined) {
            changes = {...changes, model: ''};
            setModelCatalogs((items) => {const next = {...items}; delete next[id]; return next;});
            setManualModels((items) => ({...items, [id]: false}));
        }
        setDraft((current) => ({...current, targets: current.targets.map((target) => target.id === id ? {...target, ...changes} : target)}));
        if (changes.baseURL !== undefined || changes.runtime !== undefined || changes.model !== undefined) {
            setConnectionChecks((current) => {const next = {...current}; delete next[id]; return next;});
        }
        setMessage('');
    }

    function cloneSuite() {
        setDraft((current) => ({...current, suite: {...current.suite, source: 'custom', revision: 'custom-local', reviewStatus: 'unreviewed', name: `${current.suite.name} · 내 세트`}}));
        announce('사용자 세트로 복제했습니다. 지시문, 선택지, 문항과 정답을 편집할 수 있습니다.');
    }

    function checkConfiguration() {
        const issues = [...validateDecisionSuite(suite), ...validateDecisionTargets(draft.targets)];
        if (!selectedCases.length) issues.push('선택한 유형에 실행할 문항이 없습니다.');
        if (suite.cases.some((item) => item.language !== 'ko')) issues.push('이번 실행은 한국어 문항만 지원합니다.');
        if ((draft.experimentName || '').trim().length > 100) issues.push('실험 이름은 100자 이하여야 합니다.');
        setConfigurationResult({success: issues.length === 0, text: issues.length
            ? issues.slice(0, 6).join('\n')
            : selectedTargets.length + '개 모델 · ' + selectedCases.length + '개 문항 · 요청 제한 ' + timeoutSeconds + '초. 입력 설정이 올바릅니다. 서버 연결은 각 모델의 연결 테스트로 확인하세요.'});
    }

    async function importSuite(file: File) {
        if (file.size > 5 * 1024 * 1024) { announce('5MB 이하의 JSON 또는 CSV 테스트셋을 선택해 주세요.', 'error'); return; }
        setIsImporting(true);
        try {
            const contents = await file.text();
            const imported = file.name.toLowerCase().endsWith('.csv') ? parseDecisionCSV(contents, suite, activeTask?.id) : parseDecisionSuite(contents);
            if (imported.tasks.some(task=>!evaluationTypes.includes(task.type))) throw new Error('단일 분류(choice)와 복합 후보 순위(ranking) 세트만 가져올 수 있습니다.');
            setDraft((current) => ({...current, suite: imported, types: evaluationTypes.filter(type=>imported.tasks.some(task=>task.type===type))}));
            setActiveTaskID(imported.tasks[0].id);
            setActiveCaseID('');
            setSearch('');
            onViewChange('dataset');
            announce(`${imported.name} · ${imported.cases.length}개 문항을 가져왔습니다.`);
        } catch (reason) { announce(reason instanceof Error ? reason.message : '테스트셋을 가져오지 못했습니다.', 'error'); }
        finally { setIsImporting(false); }
    }

    async function exportSuite() {
        if (exporting) return;
        const issues = validateDecisionSuite(suite);
        if (issues.length) { announce(issues.slice(0, 5).join('\n'), 'error'); return; }
        setExporting('json');
        try {
            if (await saveDatasetFile('json', `${JSON.stringify(suite, null, 2)}\n`)) announce('테스트셋 JSON을 내보냈습니다. 서버 설정과 API 키는 포함되지 않습니다.');
        } catch (reason) { announce(reason instanceof Error ? reason.message : '테스트셋을 저장하지 못했습니다.', 'error'); }
        finally { setExporting(null); }
    }

    async function exportCases() {
        if (exporting) return;
        const escape = (value: string) => `"${value.replace(/"/g, '""')}"`;
        const rows = suite.cases.map(({id, pairID, taskID, language, text, expected, instructions}) => [id, pairID, taskID, language, text, expected, instructions?.ko || ''].map(escape).join(','));
        setExporting('csv');
        try {
            if (await saveDatasetFile('csv', `\uFEFFid,pair_id,task_id,language,text,expected,instructions\r\n${rows.join('\r\n')}\r\n`)) announce('전체 문항을 CSV로 내보냈습니다. CSV에는 문항별 질문이 포함됩니다. 공통 과제 지시문·선택지 정의는 JSON에 포함됩니다.');
        } catch (reason) { announce(reason instanceof Error ? reason.message : '문항을 저장하지 못했습니다.', 'error'); }
        finally { setExporting(null); }
    }

    function addCase() {
        if (!activeTask || !editable) return;
        const pairID = crypto.randomUUID();
        updateSuite((current) => ({...current, cases: [...current.cases, ...(['ko'] as const).map((language) => ({
            id: `${pairID}-${language}`, pairID, taskID: activeTask.id, language, text: '', expected: activeTask.options[0]?.key || '',
        }))]}));
        setActiveCaseID(`${pairID}-${editLanguage}`);
        setSearch('');
    }

    function updateCase(id: string, changes: Partial<DecisionCase>) {
        updateSuite((current) => ({...current, cases: current.cases.map((item) => item.id === id ? {...item, ...changes} : item)}));
    }

    function renameOption(index: number, key: string) {
        if (!activeTask || activeTask.type !== 'choice') return;
        const oldKey = activeTask.options[index].key;
        updateSuite((current) => ({...current,
            tasks: current.tasks.map((task) => task.id === activeTask.id ? {...task, options: task.options.map((option, optionIndex) => optionIndex === index ? {...option, key} : option)} : task),
            cases: current.cases.map((item) => item.taskID === activeTask.id && item.expected === oldKey ? {...item, expected: key} : item),
        }));
    }

    function removeOption(index: number) {
        if (!activeTask || activeTask.type === 'noul' || activeTask.options.length <= 2) return;
        const removedKey = activeTask.options[index].key;
        updateSuite((current) => ({...current,
            tasks: current.tasks.map((task) => task.id === activeTask.id ? {...task, options: task.options.filter((_, optionIndex) => optionIndex !== index).map((option, optionIndex) => task.type === 'score' ? {...option, key: String(optionIndex)} : option)} : task),
            cases: current.cases.map((item) => {
                if (item.taskID !== activeTask.id) return item;
                if (item.expected === removedKey) return {...item, expected: ''};
                if (activeTask.type === 'score' && item.expected !== '' && Number(item.expected) > index) return {...item, expected: String(Number(item.expected) - 1)};
                return item;
            }),
        }));
    }

    const typeCounts = Object.fromEntries(decisionTypes.filter(type=>suite.tasks.some(task=>task.type===type)).map((type) => [type, selectedDecisionCases(suite, [type], draft.language).length])) as Record<DecisionType, number>;

    return <section className={`benchmark-page decision-page ${view === 'dataset' ? 'decision-dataset-page' : ''}`} aria-label="Decision Benchmark">
        <header className="decision-header">
            <div>
                <span className="eyebrow">MODEL LAB / DECISION</span>
                <h1>Decision Benchmark</h1>
                <p>같은 질문, 같은 기준. 모델마다 다른 판단을 비교하세요.</p>
            </div>
            <span className="decision-stage-badge"><span/>한국어 평가</span>
        </header>

        <nav className="decision-tabs" aria-label="Decision Benchmark 메뉴">
            {([['setup', '실험 설정'], ['dataset', '테스트셋'], ['results', '결과 비교']] as const).map(([tab, label]) =>
                <button key={tab} type="button" className={view === tab ? 'active' : ''} aria-current={view === tab ? 'page' : undefined} onClick={() => onViewChange(tab)}>{label}</button>)}
            <small>{saveMessage}</small>
        </nav>

        {activeRun?.status === 'running' && <div className="decision-message" role="status">실행 중 · {(activeRun.results || []).filter((r) => r.status !== 'pending').length} / {(activeRun.results || []).length}문항 <button type="button" onClick={() => void ChatService.CancelDecisionBenchmark(activeRun.id).catch((error) => announce(String(error), 'error'))}>실행 취소</button></div>}
        {message && <div className={`decision-message ${messageKind}`} role={messageKind === 'error' ? 'alert' : 'status'}>{message}</div>}

        {view === 'setup' && <>
            <section className="decision-experiment-name"><label>실험 이름 <small>선택 사항 · 실행 기록과 보고서에 표시</small><input maxLength={100} value={draft.experimentName || ''} onChange={(event) => setDraft((current) => ({...current, experimentName: event.target.value}))} placeholder="예: 업무영역 · tev1 비교"/></label></section>
            <div className="decision-summary" aria-label="선택한 실험 구성">
                <div><Icon kind="models"/><span>평가 대상<strong>{selectedTargets.length}<small>개 모델</small></strong></span></div>
                <div><Icon kind="cases"/><span>선택한 문항<strong>{selectedCases.length}<small>개 문항 · {draft.types.length}개 유형</small></strong></span></div>
                <div><Icon kind="language"/><span>평가 언어<strong className="decision-summary-language">{languageLabels[draft.language]}</strong></span></div>
            </div>

            <section className="decision-section" aria-labelledby="decision-target-heading">
                <div className="decision-section-heading">
                    <div><span className="decision-step">01</span><h2 id="decision-target-heading">평가 대상</h2><p>서버의 실제 모델 목록에서 평가할 모델을 선택합니다.</p></div>
                    <button className="secondary-button" type="button" onClick={() => {
                        const id = crypto.randomUUID();
                        setDraft((current) => ({...current, targets: [...current.targets, {id, name: '새 평가 대상', runtime: 'systemone', baseURL: '', model: '', environment: '', enabled: true}]}));
                    }}>+ 모델 추가</button>
                </div>
                <p className="decision-muted">Ollama, Bespoke Nimble API, System One 호환 서버를 각각 선택할 수 있습니다. Laya 서버는 System One 호환 서버에 해당합니다.</p>
                <div className="decision-target-grid">
                    {draft.targets.map((target, index) => <article className={`decision-target-card ${target.enabled ? 'selected' : ''}`} key={target.id}>
                        <div className="decision-target-title">
                            <span className="decision-target-letter">{String.fromCharCode(65 + index % 26)}</span>
                            <label className="decision-target-toggle"><input type="checkbox" checked={target.enabled} onChange={(event) => updateTarget(target.id, {enabled: event.target.checked})}/><span>{target.model || '평가 모델 선택'}</span></label>
                            <button type="button" className="decision-icon-button" aria-label={`${target.model || '평가 대상'} 삭제`} onClick={() => {
                                setDraft((current) => ({...current, targets: current.targets.filter((item) => item.id !== target.id)}));
                                setAPIKeys((current) => {const next = {...current}; delete next[target.id]; return next;});
                            }}>×</button>
                        </div>
                        <div className="decision-field-row">
                            <label>서버 프로그램<select value={target.runtime} onChange={(event) => updateTarget(target.id, {runtime: event.target.value as DecisionTarget['runtime']})}><option value="ollama">Ollama</option><option value="nimble">Bespoke Nimble API</option><option value="systemone">기타 Decision API 호환 서버 (System One)</option></select></label>
                        </div>
                        <label>서버 주소<input type="url" value={target.baseURL} onChange={(event) => updateTarget(target.id, {baseURL: event.target.value})} placeholder={target.runtime === 'ollama' ? 'http://localhost:11434' : 'http://192.168.0.10:12300'} spellCheck={false}/></label>
                        <div className="decision-model-picker">
                            <div className="decision-model-picker-heading"><span>평가 모델</span><button className="text-button" type="button" disabled={!target.baseURL.trim() || loadingModels.includes(target.id) || starting || activeRun?.status === 'running'} onClick={() => void loadTargetModels(target)}>{loadingModels.includes(target.id) ? '가져오는 중…' : modelCatalogs[target.id] ? '목록 새로고침' : '모델 목록 가져오기'}</button></div>
                            {manualModels[target.id] && target.runtime !== 'ollama' ? <label>모델 ID<input value={target.model} onChange={(event) => updateTarget(target.id, {model: event.target.value})} placeholder="서버에 설정된 정확한 모델 ID" spellCheck={false}/></label> : <label>설치된 모델<select value={target.model} disabled={!modelCatalogs[target.id]?.available || loadingModels.includes(target.id)} onChange={(event) => updateTarget(target.id, {model: event.target.value})}><option value="">{modelCatalogs[target.id]?.available ? '모델을 선택하세요' : '먼저 모델 목록을 가져오세요'}</option>{target.model && !(modelCatalogs[target.id]?.models || []).includes(target.model) && <option value={target.model} disabled>{target.model} · 목록 확인 필요</option>}{(modelCatalogs[target.id]?.models || []).map((id) => <option key={id} value={id}>{id}</option>)}</select></label>}
                            <small>{modelCatalogs[target.id]?.message || '서버 주소를 입력한 뒤 실제 모델 목록을 가져와 선택하세요.'}</small>
                            {target.runtime !== 'ollama' && (modelCatalogs[target.id]?.source === 'server-loaded' || modelCatalogs[target.id]?.available === false) && <label className="decision-manual-model-toggle"><input type="checkbox" checked={!!manualModels[target.id]} onChange={(event) => {setManualModels((items) => ({...items, [target.id]: event.target.checked})); updateTarget(target.id, {model: ''});}}/>모델 ID 직접 입력</label>}
                        </div>
                        <details className="decision-target-details"><summary>인증 설정</summary>
                            <label>API 키 <small>{target.runtime === 'nimble' ? 'Nimble 서버의 NIMBLE_API_KEY 값 · 저장하지 않음' : '선택 사항 · 저장하지 않음'}</small><input type="password" autoComplete="off" value={apiKeys[target.id] || ''} onChange={(event) => {setAPIKeys((current) => ({...current, [target.id]: event.target.value})); setModelCatalogs((items) => {const next = {...items}; delete next[target.id]; return next;}); setManualModels((items) => ({...items, [target.id]: false})); setConnectionChecks((items) => {const next = {...items}; delete next[target.id]; return next;});}} placeholder={target.runtime === 'nimble' ? '서버의 NIMBLE_API_KEY 입력' : '인증이 필요한 서버에서만 입력'}/></label>
                        </details>
                        <div className="decision-target-footer"><strong className={`decision-connection-badge ${connectionChecks[target.id]?.status || 'idle'}`}>{connectionChecks[target.id]?.status === 'success' ? '✓ 연결 확인' : connectionChecks[target.id]?.status === 'error' ? '! 연결 실패' : connectionChecks[target.id]?.status === 'checking' ? '확인 중…' : '연결 미확인'}</strong><button className="text-button" type="button" disabled={!target.model.trim() || !!checking || starting || activeRun?.status === 'running'} onClick={() => void checkConnection(target)}>{checking === target.id ? '확인 중…' : '연결 테스트'}</button></div>
                        {connectionChecks[target.id] && <div className={`decision-connection-result ${connectionChecks[target.id].status}`} role={connectionChecks[target.id].status === 'error' ? 'alert' : 'status'}><strong>{connectionChecks[target.id].status === 'success' ? '연결 테스트 성공' : connectionChecks[target.id].status === 'error' ? '연결 테스트 실패' : '연결 테스트 중'}</strong><span>{connectionChecks[target.id].message}</span></div>}
                    </article>)}
                    {!draft.targets.length && <div className="decision-empty-targets"><Icon kind="models"/><p>비교할 모델을 추가해 주세요.</p></div>}
                </div>
            </section>

            <section className="decision-section decision-config-card" aria-labelledby="decision-suite-heading">
                <div className="decision-section-heading"><div><span className="decision-step">02</span><h2 id="decision-suite-heading">테스트 구성</h2><p>단일 업무 분류와 복합 업무 후보 순위 중 평가할 세트를 선택합니다.</p></div><button className="text-button" type="button" onClick={() => onViewChange('dataset')}>문항 확인 <Icon kind="arrow"/></button></div>
                <div className="decision-suite-banner"><Icon kind="cases"/><div><strong>{neutralDecisionText(suite.name)}</strong><span>{suite.source === 'builtin' ? `기본 제공 · 정답 검토 전 초안 · 한국어 ${suite.cases.length}문항` : '사용자 테스트셋 · 정답과 기준을 검토해 주세요'}</span></div><span className="decision-tag">{suite.source === 'builtin' ? '기본 세트' : '사용자 세트'}</span></div>
                <div className="decision-type-grid" aria-label="평가 유형 선택">
                    {evaluationTypes.filter(type=>suite.tasks.some(task=>task.type===type)).map((type) => <label key={type} className={`decision-type-card ${draft.types.includes(type) ? 'selected' : ''}`}>
                        <div><Icon kind={type}/><input type="checkbox" checked={draft.types.includes(type)} onChange={(event) => setDraft((current) => ({...current, types: event.target.checked ? [...current.types.filter(item=>evaluationTypes.includes(item)), type] : current.types.filter((item) => evaluationTypes.includes(item) && item !== type)}))}/></div>
                        <strong>{type==='choice'?'단일 업무영역 분류':decisionTypeInfo[type].label}</strong><p>{decisionTypeInfo[type].description}</p><span>{typeCounts[type]}개 문항</span>
                    </label>)}
                </div>
                <fieldset className="decision-language-field"><legend>평가 언어</legend><div className="decision-language-options">
                    {(['ko'] as const).map((language) => <label key={language} className={draft.language === language ? 'selected' : ''}><input type="radio" name="decision-language" value={language} checked={draft.language === language} onChange={() => setDraft((current) => ({...current, language}))}/><span>{languageLabels[language]}</span>{language === 'ko' && <small>기본</small>}</label>)}
                </div></fieldset>
            </section>

            <p className="decision-muted">세트 버전 {suite.revision || '미기재'} · 출처 {neutralDecisionText(suite.sourceDocument || '사용자 입력')} · 정답 검토 {suite.reviewStatus === 'unreviewed' ? '검토 전 초안' : suite.reviewStatus || '미확인'}</p>


            <footer className="decision-launch-bar"><div><strong>{selectedTargets.length}개 모델 × {selectedCases.length}개 문항</strong><span>측정 요청 {selectedTargets.length * selectedCases.length}회 · 준비 요청 별도</span></div><div className="decision-launch-actions"><button className="secondary-button" type="button" title="입력 누락·주소 형식·문항과 정답을 검사합니다" onClick={checkConfiguration}>설정 확인</button><button className="primary-button" type="button" disabled={starting || activeRun?.status === 'running'} onClick={() => void startBenchmark()}>{starting ? '시작 중…' : '벤치마크 실행'} <Icon kind="arrow"/></button></div><label>요청 제한<select value={timeoutSeconds} onChange={(event) => setTimeoutSeconds(Number(event.target.value))}><option value={60}>기본 60초</option><option value={180}>느린 서버 180초</option></select></label><p>순차 실행 · 자동 재시도 없음 · 준비 요청은 집계 제외</p>{configurationResult && <div className={`decision-configuration-result ${configurationResult.success ? 'success' : 'error'}`} role={configurationResult.success ? 'status' : 'alert'}><strong>{configurationResult.success ? '✓ 설정 확인 완료' : '! 설정을 수정해 주세요'}</strong><span>{configurationResult.text}</span></div>}</footer>
        </>}

        {view === 'dataset' && <>
            <div className="decision-dataset-toolbar"><div><h2>{suite.name}</h2><p>{suite.cases.length}개 문항 · 한국어 평가</p></div><div className="decision-toolbar-actions"><button className="secondary-button" type="button" onClick={() => fileRef.current?.click()} disabled={isImporting || exporting !== null}>{isImporting ? '가져오는 중…' : 'JSON / CSV 가져오기'}</button><button className="secondary-button" type="button" onClick={() => void exportSuite()} disabled={exporting !== null || isImporting}>{exporting === 'json' ? '저장하는 중…' : 'JSON 내보내기'}</button><button className="text-button" type="button" onClick={() => void exportCases()} disabled={exporting !== null || isImporting}>{exporting === 'csv' ? '저장하는 중…' : 'CSV 내보내기'}</button></div></div>
            {editable && <button className="text-button" type="button" onClick={() => {setDraft((current) => ({...current, suite: createDecisionDraft().suite, language: 'ko', types: [...evaluationTypes]})); setActiveTaskID(createDecisionDraft().suite.tasks[0].id);}}>기본 세트 열기</button>}
            <div className="decision-dataset-note"><span className="decision-tag">{editable ? '사용자 세트' : '기본 세트'}</span><p>{editable ? '수정한 내용은 설정 초안으로 자동 저장됩니다. 정답과 기준을 검토해 주세요.' : '업무영역 기반 기본 제공 세트입니다. 정답은 검토 전 초안이며 복제 후 편집할 수 있습니다.'}</p>{!editable && <button className="secondary-button" type="button" onClick={cloneSuite}>사용자 세트로 복제</button>}</div>
            {editable && <label className="decision-suite-name">테스트셋 이름<input value={suite.name} onChange={(event) => updateSuite((current) => ({...current, name: event.target.value}))}/></label>}

            <div className="decision-editor-layout">
                <aside className="decision-task-list" aria-label="편집할 평가 과제">
                    <span>평가 과제</span>{suite.tasks.map((task) => <button key={task.id} type="button" className={activeTask?.id === task.id ? 'active' : ''} aria-pressed={activeTask?.id === task.id} onClick={() => {setActiveTaskID(task.id); setSearch(''); setActiveCaseID('');}}><Icon kind={task.type}/><span><strong>{task.name}</strong><small>{task.type} · {suite.cases.filter((item) => item.taskID === task.id).length}문항</small></span><span aria-hidden="true">›</span></button>)}
                    <small>각 유형의 기준과 정답을<br/>같은 조건으로 비교합니다.</small>
                </aside>
                {activeTask && <div className="decision-editor-main">
                    <details className="decision-task-definition-panel" key={activeTask.id}><summary>판단 기준 · {activeTask.name} · {activeTask.options.length}개 선택지</summary><section className="decision-task-definition"><div className="decision-section-heading"><div><h2>{activeTask.name}</h2><span className="decision-definition-meta">{activeTask.type} · {decisionTypeInfo[activeTask.type].metric}</span></div><span className="decision-tag">한국어</span></div>
                        {editable && <label>과제 이름<input value={activeTask.name} onChange={(event) => updateSuite((current) => ({...current, tasks: current.tasks.map((task) => task.id === activeTask.id ? {...task, name: event.target.value} : task)}))}/></label>}
                        <label>판단 지시문<textarea rows={3} readOnly={!editable} value={activeTask.instructions[editLanguage]} onChange={(event) => updateSuite((current) => ({...current, tasks: current.tasks.map((task) => task.id === activeTask.id ? {...task, instructions: {...task.instructions, [editLanguage]: event.target.value}} : task)}))}/></label>
                        {(activeTask.context || editable) && <label>공통 업무 설명<small>모든 문항에 함께 전달되는 참고 정보입니다. 정답과 정답 근거는 포함하지 않습니다.</small><textarea rows={8} readOnly={!editable} value={activeTask.context?.[editLanguage] || ''} onChange={(event) => updateSuite((current) => ({...current, tasks: current.tasks.map((task) => task.id === activeTask.id ? {...task, context: {...(task.context || {ko: '', en: ''}), [editLanguage]: event.target.value}} : task)}))}/></label>}
                        <div className="decision-option-heading"><span>{activeTask.type === 'score' ? '등급 기준 · 낮은 등급부터' : '선택지와 판단 기준'}</span><small>{activeTask.options.length}개</small></div>
                        <div className="decision-options-editor">{activeTask.options.map((option, index) => <div key={index}>
                            {editable && activeTask.type === 'choice' ? <input aria-label={`${index + 1}번째 선택지 키`} value={option.key} onChange={(event) => renameOption(index, event.target.value)} spellCheck={false}/> : <code>{option.key}</code>}
                            <textarea aria-label={`${option.key} ${languageLabels[editLanguage]} 판단 기준`} rows={2} readOnly={!editable} value={option.descriptions[editLanguage]} onChange={(event) => updateSuite((current) => ({...current, tasks: current.tasks.map((task) => task.id === activeTask.id ? {...task, options: task.options.map((entry, optionIndex) => optionIndex === index ? {...entry, descriptions: {...entry.descriptions, [editLanguage]: event.target.value}} : entry)} : task)}))}/>
                            {editable && activeTask.type !== 'noul' && <button className="decision-icon-button" type="button" disabled={activeTask.options.length <= 2} aria-label={`${option.key} ${activeTask.type === 'score' ? '등급' : '선택지'} 삭제`} onClick={() => removeOption(index)}>×</button>}
                        </div>)}</div>
                        {editable && activeTask.type !== 'noul' && <button className="text-button decision-add-option" type="button" disabled={activeTask.options.length >= 26} onClick={() => updateSuite((current) => ({...current, tasks: current.tasks.map((task) => {
                            if (task.id !== activeTask.id) return task;
                            const key = task.type === 'score' ? String(task.options.length) : `label_${crypto.randomUUID().slice(0, 8)}`;
                            return {...task, options: [...task.options, {key, descriptions: {ko: '', en: ''}}]};
                        })}))}>+ {activeTask.type === 'score' ? '등급' : '선택지'} 추가</button>}
                    </section></details>
                    <section className="decision-cases-section"><div className="decision-section-heading"><div><h2>테스트 문항 <small>{visibleCases.length}</small></h2></div><button className="secondary-button" type="button" disabled={!editable} onClick={addCase}>+ 문항 추가</button></div>
                        <input className="decision-case-search" type="search" aria-label="테스트 문항 검색" placeholder="문항 내용 · ID · 정답 검색" value={search} onChange={(event) => setSearch(event.target.value)}/>
                        <div className="decision-cases-workspace"><div className="decision-case-list" key={activeTask.id} role="region" aria-label="테스트 문항 목록" tabIndex={0}>{visibleCases.map((item, index) => <button type="button" key={item.id} className={activeCase?.id === item.id ? 'active' : ''} aria-pressed={activeCase?.id === item.id} onClick={() => setActiveCaseID(item.id)}><span className="decision-case-index">{String(index + 1).padStart(2, '0')}</span><span>{item.text || '새 문항 · 텍스트를 입력하세요'}</span><code>{expectedLabel(item, suite)}</code></button>)}{!visibleCases.length && <p className="decision-muted">표시할 문항이 없습니다.</p>}</div>
                        {activeCase && <div className="decision-case-editor" key={activeCase.id} role="region" aria-label="선택한 문항 상세" tabIndex={0}><h3>선택한 문항</h3><div><span className="decision-tag">{activeCase.language.toUpperCase()}</span><code title={activeCase.pairID}>{activeCase.id}</code>{editable && <button className="text-button danger" type="button" onClick={() => updateSuite((current) => ({...current, cases: current.cases.filter((item) => item.id !== activeCase.id)}))}>이 문항 삭제</button>}</div>{(activeCase.instructions || editable) && <label>이 문항의 판단 질문<small>후보 업무 정의를 질문에 넣습니다. 입력 텍스트에는 실제 요청만 넣습니다.</small><textarea rows={3} readOnly={!editable} value={activeCase.instructions?.[editLanguage] || ''} onChange={(event)=>updateCase(activeCase.id,{instructions:{...(activeCase.instructions || {ko:'',en:''}),[editLanguage]:event.target.value}})}/></label>}<label>입력 텍스트<textarea rows={8} readOnly={!editable} value={activeCase.text} onChange={(event) => updateCase(activeCase.id, {text: event.target.value})}/></label>{activeTask.type==='ranking'?<label>정답 업무영역 목록<small>예: D04|D05 · 순서는 채점하지 않습니다. 상위 목록 크기는 정답 개수로 정합니다.</small><input readOnly={!editable} value={activeCase.expected} onChange={event=>updateCase(activeCase.id,{expected:event.target.value})}/></label>:<label>기대 정답<select value={activeCase.expected} disabled={!editable} onChange={(event) => updateCase(activeCase.id, {expected: event.target.value})}><option value="">정답을 선택하세요</option>{activeTask.options.map((option) => <option key={option.key} value={option.key}>{option.key} · {option.descriptions[editLanguage]}</option>)}</select></label>}<small>정답은 채점에만 사용하며 모델 요청에는 포함되지 않습니다.</small></div>}</div>
                    </section>
                </div>}
            </div>
        </>}

        {view === 'results' && <DecisionBenchmarkResults activeRun={activeRun} refreshKey={resultsRefresh} onError={(error) => announce(error, 'error')}/> }
        <input ref={fileRef} type="file" accept=".json,.csv,application/json,text/csv" hidden aria-label="Decision 테스트셋 JSON 파일" onChange={(event) => {const file = event.target.files?.[0]; event.target.value = ''; if (file) void importSuite(file);}}/>
    </section>;
}
