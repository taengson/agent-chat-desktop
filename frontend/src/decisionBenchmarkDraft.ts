import detailedDomainSuite from './datasets/domainDecisionSuiteDetailed.json' with {type: 'json'};
import domainSuite from './datasets/domainDecisionSuite.json' with {type: 'json'};

export type DecisionType = 'choice' | 'ranking' | 'noul' | 'score';
export type DecisionLanguage = 'ko' | 'en';
export type DecisionLanguageSelection = DecisionLanguage | 'both';

export interface DecisionOption {
    key: string;
    descriptions: Record<DecisionLanguage, string>;
}

export interface DecisionTask {
    context?: Record<DecisionLanguage, string>;
    id: string;
    type: DecisionType;
    name: string;
    instructions: Record<DecisionLanguage, string>;
    options: DecisionOption[];
}

export interface DecisionCase {
    instructions?: Record<DecisionLanguage, string>;
    id: string;
    pairID: string;
    taskID: string;
    language: DecisionLanguage;
    text: string;
    expected: string;
    scenarioID?: string;
    rationale?: string;
    candidate?: string;
    reviewStatus?: string;
}

export interface DecisionSuite {
    questionStyle?: 'compact' | 'detailed';
    format: 'decision-benchmark-suite';
    version: 1;
    name: string;
    source: 'example' | 'custom' | 'builtin';
    id?: string;
    revision?: string;
    reviewStatus?: string;
    sourceDocument?: string;
    sourceHash?: string;
    scoringVersion?: string;
    tasks: DecisionTask[];
    cases: DecisionCase[];
}

export interface DecisionTarget {
    id: string;
    name: string;
    runtime: 'laya' | 'ollama' | 'systemone' | 'nimble';
    baseURL: string;
    model: string;
    environment: string;
    enabled: boolean;
}

export interface DecisionDraft {
    experimentName?: string;
    version: 1;
    suite: DecisionSuite;
    targets: DecisionTarget[];
    language: DecisionLanguageSelection;
    types: DecisionType[];
}

export const decisionTypeInfo: Record<DecisionType, {label: string; description: string; metric: string}> = {
    ranking: {label: '복합 업무 후보 순위', description: '필요한 업무들이 상위 목록에 포함되는지 평가합니다.', metric: '상위 목록 일치율 · 정답 영역 회수율'},
    choice: {label: '선택형 분류', description: '정해진 선택지 중 하나를 판단합니다.', metric: '정확도 · Macro F1'},
    noul: {label: '예 / 아니오', description: '주어진 조건이 참인지 판단합니다.', metric: '정확도 · 오탐 / 누락'},
    score: {label: '등급 평가', description: '기준에 따라 순서가 있는 등급을 판단합니다.', metric: '등급 일치율 · 등급 오차'},
};
export const decisionTypes: DecisionType[] = ['choice', 'ranking', 'noul', 'score'];
export const decisionDraftStorageKey = 'agent-chat.decision-benchmark-draft.v1';

export function createExampleDecisionDraft(): DecisionDraft {
    const tasks: DecisionTask[] = [
        {
            id: 'sentiment', type: 'choice', name: '문장 감정 분류',
            instructions: {ko: '문장이 표현하는 감정을 선택하세요.', en: 'Choose the sentiment expressed by the text.'},
            options: [
                {key: 'positive', descriptions: {ko: '만족, 칭찬 또는 긍정적인 경험', en: 'Satisfaction, praise, or a positive experience'}},
                {key: 'neutral', descriptions: {ko: '감정 없이 사실이나 정보를 전달함', en: 'Facts or information without an emotional judgment'}},
                {key: 'negative', descriptions: {ko: '불만, 비판 또는 부정적인 경험', en: 'Dissatisfaction, criticism, or a negative experience'}},
            ],
        },
        {
            id: 'return-policy', type: 'noul', name: '반품 조건 판단',
            instructions: {
                ko: '구매 후 7일 이내이고 미개봉인 경우에만 반품할 수 있습니다. 입력의 상품은 반품 가능한가요?',
                en: 'A return is allowed only within 7 days of purchase and if the item is unopened. Is the item eligible?',
            },
            options: [
                {key: 'false', descriptions: {ko: '기간 또는 미개봉 조건을 충족하지 않음', en: 'The time limit or unopened condition is not met'}},
                {key: 'true', descriptions: {ko: '기간과 미개봉 조건을 모두 충족함', en: 'Both the time limit and unopened condition are met'}},
            ],
        },
        {
            id: 'urgency', type: 'score', name: '문의 긴급도',
            instructions: {ko: '아래 기준에 따라 문의의 긴급도를 평가하세요.', en: 'Rate the urgency using the criteria below.'},
            options: [
                {key: '0', descriptions: {ko: '일반: 정보 문의이며 서비스 이용에 문제가 없음', en: 'Routine: an information request with no service disruption'}},
                {key: '1', descriptions: {ko: '빠른 대응: 일부 기능이 불편하지만 대체 방법이 있음', en: 'Soon: a feature is impaired but a workaround exists'}},
                {key: '2', descriptions: {ko: '즉시 대응: 핵심 서비스가 중단되어 대체 방법이 없음', en: 'Immediate: a core service is unavailable with no workaround'}},
            ],
        },
    ];
    const samples: Array<[string, string, string, string, string]> = [
        ['sentiment', 'sentiment-positive', '배송이 빠르고 품질도 훌륭해서 정말 만족합니다.', 'Delivery was fast and the quality was excellent. I am very satisfied.', 'positive'],
        ['sentiment', 'sentiment-negative', '제품이 고장 난 상태로 도착해서 너무 실망했습니다.', 'The product arrived broken and I am very disappointed.', 'negative'],
        ['return-policy', 'return-eligible', '구매한 지 3일 되었고 포장을 개봉하지 않았습니다.', 'The item was purchased 3 days ago and has not been opened.', 'true'],
        ['return-policy', 'return-ineligible', '구매한 지 10일 되었고 포장을 개봉하지 않았습니다.', 'The item was purchased 10 days ago and has not been opened.', 'false'],
        ['urgency', 'urgency-routine', '서비스는 정상 작동합니다. 다음 업데이트 일정을 알려주세요.', 'The service works normally. Please tell me the date of the next update.', '0'],
        ['urgency', 'urgency-immediate', '모든 사용자가 로그인할 수 없고 다른 접속 방법도 없습니다.', 'No users can log in and there is no alternative way to access the service.', '2'],
    ];
    return {
        version: 1, language: 'ko', types: [...decisionTypes],
        suite: {
            format: 'decision-benchmark-suite', version: 1, name: 'Decision 기본 구성 예제', source: 'example', tasks,
            cases: samples.flatMap(([taskID, pairID, ko, en, expected]) => [
                {id: `${pairID}-ko`, pairID, taskID, language: 'ko' as const, text: ko, expected},
                {id: `${pairID}-en`, pairID, taskID, language: 'en' as const, text: en, expected},
            ]),
        },
        targets: [
            {id: 'laya-spark', name: 'Laya Multilingual', runtime: 'laya', baseURL: '', model: 'multilingual', environment: 'DGX Spark · GB10 · CUDA', enabled: true},
            {id: 'ollama-nimble', name: 'Nimble', runtime: 'ollama', baseURL: 'http://localhost:11434', model: 'nimble', environment: '', enabled: true},
        ],
    };
}

export function createDecisionSuite(style: 'compact' | 'detailed' = 'compact'): DecisionSuite {
    return structuredClone(style === 'detailed' ? detailedDomainSuite : domainSuite) as DecisionSuite;
}

export function createDecisionDraft(): DecisionDraft {
    return {...createExampleDecisionDraft(), suite: createDecisionSuite(), types: ['choice','ranking'], language: 'ko', experimentName: '', targets: createExampleDecisionDraft().targets.map((target) => ({...target, runtime: target.runtime === 'laya' ? 'systemone' as const : target.runtime, name: target.model, environment: ''}))};
}

function isObject(value: unknown): value is Record<string, unknown> {
    return typeof value === 'object' && value !== null && !Array.isArray(value);
}
function isBilingual(value: unknown): value is Record<DecisionLanguage, string> {
    return isObject(value) && typeof value.ko === 'string' && typeof value.en === 'string';
}
function hasSuiteShape(value: unknown): value is DecisionSuite {
    return isObject(value) && value.format === 'decision-benchmark-suite' && value.version === 1
        && typeof value.name === 'string' && (['example', 'custom', 'builtin'].includes(value.source as string))
        && Array.isArray(value.tasks) && value.tasks.every((task) => isObject(task)
            && typeof task.id === 'string' && typeof task.name === 'string' && decisionTypes.includes(task.type as DecisionType)
            && isBilingual(task.instructions) && (task.context === undefined || isBilingual(task.context)) && Array.isArray(task.options)
            && task.options.every((option) => isObject(option) && typeof option.key === 'string' && isBilingual(option.descriptions)))
        && Array.isArray(value.cases) && value.cases.every((item) => isObject(item)
            && ['id', 'pairID', 'taskID', 'text', 'expected'].every((key) => typeof item[key] === 'string')
            && (item.language === 'ko' || item.language === 'en') && (item.instructions === undefined || isBilingual(item.instructions)));
}

export function validateDecisionSuite(suite: DecisionSuite): string[] {
    const issues: string[] = [];
    if (!suite.name.trim()) issues.push('테스트셋 이름을 입력해 주세요.');
    if (suite.tasks.length === 0) issues.push('평가 과제가 하나 이상 필요합니다.');
    if (suite.cases.length === 0) issues.push('테스트 문항이 하나 이상 필요합니다.');
    const taskIDs = new Set<string>();
    for (const task of suite.tasks) {
        if (!task.id.trim() || taskIDs.has(task.id)) issues.push(`과제 ID가 비어 있거나 중복됩니다: ${task.id}`);
        taskIDs.add(task.id);
        if (!task.name.trim()) issues.push('과제 이름을 입력해 주세요.');
        const keys = task.options.map((option) => option.key);
        if (keys.some((key) => !key.trim()) || new Set(keys).size !== keys.length) issues.push(`${task.name}: 선택지 키가 비어 있거나 중복됩니다.`);
        if (keys.length < 2 || keys.length > 26) issues.push(`${task.name}: 선택지 또는 등급은 2~26개여야 합니다.`);
        if (task.type === 'noul' && (keys.length !== 2 || !keys.includes('true') || !keys.includes('false'))) issues.push(`${task.name}: true / false 선택지가 필요합니다.`);
        if (task.type === 'score' && keys.some((key, index) => key !== String(index))) issues.push(`${task.name}: 등급 번호는 0부터 순서대로 지정해야 합니다.`);
        for (const language of (['ko', 'en'] as const).filter((language) => suite.cases.some((item) => item.language === language))) {
            if (!task.instructions[language].trim() || task.options.some((option) => !option.descriptions[language].trim())) issues.push(`${task.name}: ${language === 'ko' ? '한국어' : '영어'} 지시문과 선택지 설명을 채워 주세요.`);
        }
    }
    const ids = new Set<string>();
    const pairs = new Set<string>();
    for (const item of suite.cases) {
        if (!item.id.trim() || ids.has(item.id)) issues.push(`문항 ID가 비어 있거나 중복됩니다: ${item.id}`);
        ids.add(item.id);
        const task = suite.tasks.find((entry) => entry.id === item.taskID);
        if (!task) issues.push(`${item.id}: 등록되지 않은 과제입니다.`);
        else if (!(task.type==='ranking' ? item.expected.split('|').length>=2 && new Set(item.expected.split('|')).size===item.expected.split('|').length && item.expected.split('|').every(key=>task.options.some(o=>o.key===key)) : task.options.some((option) => option.key === item.expected))) issues.push(`${item.id}: 정답이 선택지 또는 등급에 없습니다.`);
        if (!item.text.trim()) issues.push(`${item.id}: 입력 텍스트가 비어 있습니다.`);
        if (item.pairID) {
            const pairKey = JSON.stringify([item.taskID, item.pairID, item.language]);
            if (pairs.has(pairKey)) issues.push(`${item.id}: 같은 과제·언어의 대응 문항 ID가 중복됩니다.`);
            pairs.add(pairKey);
        }
    }
    return issues;
}

export function parseDecisionSuite(contents: string): DecisionSuite {
    let value: unknown;
    try { value = JSON.parse(contents.replace(/^\uFEFF/, '')); }
    catch { throw new Error('올바른 JSON 파일이 아닙니다.'); }
    if (!hasSuiteShape(value)) throw new Error('지원하는 Decision 테스트셋 형식(v1)이 아닙니다.');
    const issues = validateDecisionSuite(value);
    if (issues.length) throw new Error(issues.slice(0, 5).join('\n'));
    // Reconstruct known fields, rather than retaining arbitrary imported metadata.
    return {
        format: 'decision-benchmark-suite', version: 1, name: value.name, source: 'custom',
        id: typeof value.id === 'string' ? value.id : undefined,
        revision: typeof value.revision === 'string' ? value.revision : undefined,
        questionStyle: value.questionStyle === 'compact' || value.questionStyle === 'detailed' ? value.questionStyle : undefined,
        sourceDocument: typeof value.sourceDocument === 'string' ? value.sourceDocument : undefined,
        sourceHash: typeof value.sourceHash === 'string' ? value.sourceHash : undefined,
        scoringVersion: typeof value.scoringVersion === 'string' ? value.scoringVersion : undefined,
        reviewStatus: 'unreviewed',
        tasks: value.tasks.map(({id, type, name, instructions, options, context}) => ({id, type, name,
            ...(context ? {context: {ko: context.ko, en: context.en}} : {}),
            instructions: {ko: instructions.ko, en: instructions.en},
            options: options.map(({key, descriptions}) => ({key, descriptions: {ko: descriptions.ko, en: descriptions.en}})),
        })),
        cases: value.cases.map(({id, pairID, taskID, language, text, expected, scenarioID, rationale, candidate, instructions}) => ({id, pairID, taskID, language, text, expected,
            ...(instructions ? {instructions: {ko: instructions.ko, en: instructions.en}} : {}),
            scenarioID: typeof scenarioID === 'string' ? scenarioID : undefined,
            rationale: typeof rationale === 'string' ? rationale : undefined,
            candidate: typeof candidate === 'string' ? candidate : undefined, reviewStatus: 'unreviewed'})),
    };
}

export function restoreDecisionDraft(contents: string): DecisionDraft {
    const value: unknown = JSON.parse(contents);
    if (!isObject(value) || (value.experimentName !== undefined && typeof value.experimentName !== 'string') || value.version !== 1 || !hasSuiteShape(value.suite)
        || !['ko', 'en', 'both'].includes(value.language as string)
        || !Array.isArray(value.types) || !value.types.every((type) => decisionTypes.includes(type))
        || !Array.isArray(value.targets) || !value.targets.every((target) => isObject(target)
            && ['id', 'name', 'baseURL', 'model', 'environment'].every((key) => typeof target[key] === 'string')
            && ['laya', 'ollama', 'systemone', 'nimble'].includes(target.runtime as string) && typeof target.enabled === 'boolean')) {
        throw new Error('설정 초안을 읽을 수 없습니다.');
    }
    const draft = value as unknown as DecisionDraft;
    if (draft.suite.source === 'example' || draft.suite.source === 'builtin' && (draft.suite.id === 'mybee-domain-pilot' || !draft.suite.questionStyle || ['0.3.0-draft-compact','0.4.0-draft-compact','0.3.0-draft-detailed','0.5.0-draft-compact','0.5.0-draft-detailed','0.6.0-draft-compact','0.6.0-draft-detailed'].includes(draft.suite.revision || ''))) {draft.suite = createDecisionSuite(draft.suite.questionStyle === 'detailed' ? 'detailed' : 'compact'); draft.language = 'ko';draft.types=['choice','ranking'];}
    if (!draft.suite.cases.some((item) => item.language === 'en')) draft.language = 'ko';
    draft.targets = draft.targets.map((target) => target.runtime === 'laya' ? {...target, runtime: 'systemone'} : target);
    draft.suite = {...draft.suite, name: neutralDecisionText(draft.suite.name)};
    if (typeof draft.suite.sourceDocument === 'string') draft.suite.sourceDocument = neutralDecisionText(draft.suite.sourceDocument);
    if (draft.experimentName) draft.experimentName = neutralDecisionText(draft.experimentName);
    return draft;
}

export function selectedDecisionCases(suite: DecisionSuite, types: DecisionType[], language: DecisionLanguageSelection): DecisionCase[] {
    const taskIDs = new Set(suite.tasks.filter((task) => types.includes(task.type)).map((task) => task.id));
    return suite.cases.filter((item) => taskIDs.has(item.taskID) && (language === 'both' || item.language === language));
}

export function buildSystemOnePreview(suite: DecisionSuite, item: DecisionCase, model: string) {
    const task = suite.tasks.find((entry) => entry.id === item.taskID);
    if (!task) throw new Error('문항에 연결된 과제가 없습니다.');
    return {
        model: model.trim(), state: {body: task.context?.[item.language]?.trim() ? task.context[item.language].trim() + '\n\n' + item.text : item.text},
        questions: {
            decision: {
                type: task.type === 'ranking' ? 'choice' : task.type, instructions: item.instructions?.[item.language]?.trim() || task.instructions[item.language],
                criteria: task.type === 'score'
                    ? task.options.map((option) => option.descriptions[item.language])
                    : Object.fromEntries(task.options.map((option) => [option.key, option.descriptions[item.language]])),
            },
        },
    };
}

export function validateDecisionTargets(targets: DecisionTarget[]): string[] {
    const selected = targets.filter((target) => target.enabled);
    if (!selected.length) return ['평가 대상 모델을 하나 이상 선택해 주세요.'];
    return selected.flatMap((target) => {
        const issues: string[] = [];
        if (!target.model.trim()) issues.push(`${target.name || '평가 대상'}: 모델을 선택해 주세요.`);
        try {
            const url = new URL(target.baseURL);
            const endpoint = target.runtime === 'nimble' ? '/decide' : '/v1/systemone';
            if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || url.pathname.endsWith(endpoint)) throw new Error();
        } catch { issues.push(`${target.name || '평가 대상'}: http(s) 서버 기본 주소를 입력해 주세요. 호출 경로와 인증 정보는 제외합니다.`); }
        return issues;
    });
}


// Import all rows or none; physical line numbers survive quoted multiline cells.
export function parseDecisionCSV(contents: string, suite: DecisionSuite, defaultTaskID = ''): DecisionSuite {
    const text = contents.replace(/^\uFEFF/, '');
    const records: Array<{cells: string[]; line: number}> = [];
    let cells: string[] = [], field = '', quoted = false, closed = false, line = 1, rowLine = 1;
    for (let i = 0; i <= text.length; i++) {
        const ch = text[i];
        if (quoted) {
            if (ch === undefined) throw new Error(rowLine + '행: 닫히지 않은 따옴표입니다.');
            if (ch === '"') {if (text[i + 1] === '"') {field += '"'; i++;} else {quoted = false; closed = true;}}
            else {field += ch; if (ch === '\n') line++;}
            continue;
        }
        if (ch === '"' && !field && !closed) {quoted = true; continue;}
        if (ch === ',' || ch === '\n' || ch === '\r' || ch === undefined) {
            cells.push(field); field = ''; closed = false;
            if (ch !== ',') {
                if (cells.some(c => c !== '')) records.push({cells, line: rowLine});
                cells = [];
                if (ch === '\r' && text[i + 1] === '\n') i++;
                line++; rowLine = line;
            }
            continue;
        }
        if (closed || ch === '"') throw new Error(line + '행: 따옴표 형식이 올바르지 않습니다.');
        field += ch;
    }
    if (records.length < 2) throw new Error('헤더와 문항이 필요합니다.');
    const headers = records[0].cells.map(c => c.trim());
    const allowed = ['id', 'pair_id', 'task_id', 'language', 'text', 'expected', 'instructions'];
    if (new Set(headers).size !== headers.length || headers.some(h => !allowed.includes(h)) || ['id','text','expected'].some(h => !headers.includes(h))) throw new Error('CSV 헤더를 확인해 주세요: id,pair_id,task_id,language,text,expected');
    const imported: DecisionCase[] = records.slice(1).map(({cells, line}) => {
        if (cells.length !== headers.length) throw new Error(line + '행: 열 개수가 헤더와 다릅니다.');
        const row = Object.fromEntries(headers.map((h,i) => [h,cells[i]]));
        const taskID = row.task_id?.trim() || defaultTaskID;
        const language = row.language?.trim() || 'ko';
        if (language !== 'ko') throw new Error(line + '행: 이번 가져오기는 한국어(ko)만 지원합니다.');
        const task = suite.tasks.find(t=>t.id===taskID);
        if (!task) throw new Error(line + '행: 등록되지 않은 과제입니다.');
        if (!row.id.trim() || !row.text.trim() || !(task.type==='ranking' ? row.expected.trim().split('|').length>=2 && row.expected.trim().split('|').every(key=>task.options.some(o=>o.key===key)) : task.options.some(o=>o.key===row.expected.trim()))) throw new Error(line + '행: ID·본문·정답을 확인해 주세요.');
        return {id:row.id.trim(), pairID:row.pair_id?.trim() || '', taskID, language, text:row.text, expected:row.expected.trim(), ...(row.instructions?.trim() ? {instructions:{ko:row.instructions,en:''}} : {}), reviewStatus:'unreviewed'};
    });
    const idRows=new Map<string,number>(), pairRows=new Map<string,number>();
    imported.forEach((c,i)=>{const line=records[i+1].line;const pair=c.taskID+'\u0000'+c.pairID;
        if(idRows.has(c.id)) throw new Error(line+'행: 문항 ID 중복 ('+idRows.get(c.id)+'행).');
        if(c.pairID && pairRows.has(pair)) throw new Error(line+'행: 대응 문항 ID 중복 ('+pairRows.get(pair)+'행).');
        idRows.set(c.id,line);if(c.pairID)pairRows.set(pair,line);
    });
    const result:DecisionSuite={...suite, source:'custom', revision:'custom-local', reviewStatus:'unreviewed', cases:imported};
    const issues=validateDecisionSuite(result);if(issues.length)throw new Error(issues.join('\n'));
    return result;
}

export function neutralDecisionText(text: string): string {
    return text.replace(/mybee/gi, '업무영역');
}
