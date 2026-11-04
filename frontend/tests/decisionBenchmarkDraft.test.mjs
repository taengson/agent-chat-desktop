import assert from 'node:assert/strict';
import test from 'node:test';
import {
    buildSystemOnePreview,
    createExampleDecisionDraft as createDecisionDraft,
    parseDecisionSuite,
    restoreDecisionDraft,
    selectedDecisionCases,
    validateDecisionSuite,
    validateDecisionTargets,
} from '../.vite/test/decisionBenchmarkDraft.js';

test('the example contains all three decision types and aligned Korean/English pairs', () => {
    const {suite} = createDecisionDraft();
    assert.deepEqual(validateDecisionSuite(suite), []);
    assert.equal(selectedDecisionCases(suite, ['choice', 'noul', 'score'], 'ko').length, 6);
    assert.equal(selectedDecisionCases(suite, ['choice', 'noul', 'score'], 'both').length, 12);
    assert.equal(selectedDecisionCases(suite, ['score'], 'en').length, 2);
    for (const item of suite.cases.filter((entry) => entry.language === 'ko')) {
        const pair = suite.cases.find((entry) => entry.pairID === item.pairID && entry.language === 'en');
        assert.ok(pair);
        assert.equal(pair.expected, item.expected);
        assert.equal(pair.taskID, item.taskID);
    }
});

test('System One preview uses localized choice criteria and never includes the answer', () => {
    const {suite} = createDecisionDraft();
    const item = suite.cases.find((entry) => entry.language === 'en' && entry.taskID === 'sentiment');
    const request = buildSystemOnePreview(suite, item, ' nimble ');
    assert.equal(request.model, 'nimble');
    assert.deepEqual(request.state, {body: item.text});
    assert.equal(request.questions.decision.instructions, suite.tasks[0].instructions.en);
    assert.deepEqual(Object.keys(request.questions.decision.criteria), ['positive', 'neutral', 'negative']);
    assert.equal(JSON.stringify(request).includes('expected'), false);
    assert.equal(JSON.stringify(request).includes('pairID'), false);
});

test('noul preserves boolean slots while score serializes an ordered list, not a choice map', () => {
    const {suite} = createDecisionDraft();
    const noul = suite.cases.find((entry) => entry.taskID === 'return-policy');
    const score = suite.cases.find((entry) => entry.taskID === 'urgency');
    const binaryRequest = buildSystemOnePreview(suite, noul, 'multilingual');
    const scoreRequest = buildSystemOnePreview(suite, score, 'multilingual');
    assert.deepEqual(Object.keys(binaryRequest.questions.decision.criteria), ['false', 'true']);
    assert.equal(scoreRequest.questions.decision.type, 'score');
    assert.deepEqual(scoreRequest.questions.decision.criteria, suite.tasks[2].options.map((option) => option.descriptions.ko));
});

test('JSON roundtrip imports as a custom suite and does not retain unrelated secrets or metadata', () => {
    const {suite} = createDecisionDraft();
    const imported = parseDecisionSuite(`\uFEFF${JSON.stringify({...suite, apiKey: 'not-a-real-key', targets: [{apiKey: 'not-a-real-key'}]})}`);
    assert.equal(imported.source, 'custom');
    assert.deepEqual(imported.cases.map(({scenarioID, rationale, candidate, reviewStatus, ...c}) => c), suite.cases);
    assert.ok(imported.cases.every(c => c.reviewStatus === 'unreviewed'));
    assert.deepEqual(imported.tasks, suite.tasks);
    assert.equal('apiKey' in imported, false);
    assert.equal('targets' in imported, false);
});

test('import rejects malformed structures, unsupported versions and wrong answer labels', () => {
    assert.throws(() => parseDecisionSuite('{'), /JSON/);
    const {suite} = createDecisionDraft();
    assert.throws(() => parseDecisionSuite(JSON.stringify({...suite, version: 2})), /v1/);
    assert.throws(() => parseDecisionSuite(JSON.stringify({...suite, tasks: [{id: 'invalid'}]})), /형식/);
    suite.cases[0].expected = 'missing-label';
    assert.throws(() => parseDecisionSuite(JSON.stringify(suite)), /정답/);
});

test('import rejects duplicate IDs and duplicate language-specific counterparts atomically', () => {
    const {suite} = createDecisionDraft();
    const duplicate = {...suite.cases[0], id: 'new-id'};
    assert.throws(() => parseDecisionSuite(JSON.stringify({...suite, cases: [...suite.cases, duplicate]})), /대응 문항/);
    assert.throws(() => parseDecisionSuite(JSON.stringify({...suite, cases: [...suite.cases, {...duplicate, pairID: 'different-pair', id: suite.cases[0].id}]})), /문항 ID/);
});

test('noul slots and ordinal level order are validated rather than silently reinterpreted', () => {
    const {suite} = createDecisionDraft();
    suite.tasks[1].options[0].key = 'no';
    suite.tasks[2].options.reverse();
    const issues = validateDecisionSuite(suite);
    assert.ok(issues.some((entry) => entry.includes('true / false')));
    assert.ok(issues.some((entry) => entry.includes('등급 번호')));
});

test('draft restore preserves incomplete edits while imports require complete cases', () => {
    const draft = createDecisionDraft();
    draft.suite.source = 'custom';
    draft.targets = draft.targets.map(t => t.runtime === 'laya' ? {...t, runtime: 'systemone'} : t);
    draft.suite.cases[0].text = '';
    assert.deepEqual(restoreDecisionDraft(JSON.stringify(draft)), draft);
    assert.throws(() => parseDecisionSuite(JSON.stringify(draft.suite)), /비어/);
    assert.throws(() => restoreDecisionDraft(JSON.stringify({...draft, language: 'ja'})), /설정 초안/);
});

test('target configuration accepts private host URLs, checks only selected models, and rejects credentials in URLs', () => {
    const targets = createDecisionDraft().targets;
    targets[0].baseURL = 'http://spark-f8df:12300';
    assert.deepEqual(validateDecisionTargets(targets), []);
    targets[0].runtime = 'nimble';
    assert.deepEqual(validateDecisionTargets(targets), []);
    targets[0].baseURL = 'http://spark-f8df:12300/decide';
    assert.equal(validateDecisionTargets(targets).length, 1);
    targets[0].baseURL = 'http://user:password@spark-f8df:12300';
    assert.equal(validateDecisionTargets(targets).length, 1);
    targets[0].enabled = false;
    assert.deepEqual(validateDecisionTargets(targets), []);
    targets[1].baseURL = 'http://localhost:11434/v1/systemone';
    assert.equal(validateDecisionTargets(targets).length, 1);
    targets[1].enabled = false;
    assert.equal(validateDecisionTargets(targets).length, 1);
});
