import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createDecisionSuite,createDecisionDraft,createExampleDecisionDraft,restoreDecisionDraft,parseDecisionCSV,parseDecisionSuite,validateDecisionSuite,buildSystemOnePreview} from '../.vite/test/decisionBenchmarkDraft.js';

test('the default is exactly the agreed Korean question set, with no answer metadata in requests',async()=>{
 const draft=createDecisionDraft();assert.equal(draft.language,'ko');assert.equal(draft.suite.source,'builtin');assert.equal(draft.suite.cases.length,40);assert.deepEqual(validateDecisionSuite(draft.suite),[]);
 const counts={};for(const c of draft.suite.cases){assert.equal(c.language,'ko');const task=draft.suite.tasks.find(t=>t.id===c.taskID);counts[task.type]=(counts[task.type]||0)+1;const preview=buildSystemOnePreview(draft.suite,c,'multilingual');const keys=Object.keys(preview);assert.deepEqual(keys,['model','state','questions']);assert.equal(JSON.stringify(preview).includes(c.id),false);assert.equal('expected' in preview.state,false);}
 assert.deepEqual(counts,{choice:20,ranking:20});
 assert.equal(draft.suite.tasks[1].type,'ranking');

});
test('legacy examples migrate while custom work and target profiles survive',()=>{
 const old=createExampleDecisionDraft();old.targets[0].baseURL='http://spark:12300';const migrated=restoreDecisionDraft(JSON.stringify(old));assert.equal(migrated.suite.cases.length,40);assert.deepEqual(migrated.targets,old.targets.map(t=>t.runtime==='laya'?{...t,runtime:'systemone'}:t));
 old.suite.source='custom';old.targets=old.targets.map(t=>t.runtime==='laya'?{...t,runtime:'systemone'}:t);assert.deepEqual(restoreDecisionDraft(JSON.stringify(old)),old);
});
test('Bespoke Nimble server selections survive draft storage',()=>{
 const draft=createDecisionDraft();draft.targets=[{...draft.targets[0],runtime:'nimble',baseURL:'http://spark:12300',model:'Bespoke-Nimble-9B'}];
 assert.deepEqual(restoreDecisionDraft(JSON.stringify(draft)).targets,draft.targets);
});
test('JSON roundtrip preserves known provenance without accepting review completion or secrets',()=>{
 const s=createDecisionDraft().suite;const copy=parseDecisionSuite(JSON.stringify({...s,reviewStatus:'approved',apiKey:'secret'}));assert.equal(copy.sourceDocument,s.sourceDocument);assert.equal(copy.reviewStatus,'unreviewed');assert.equal(copy.cases[0].rationale,s.cases[0].rationale);assert.equal('apiKey' in copy,false);
});
test('CSV supports BOM, quoting, multiline Korean text, and default task',()=>{
 const s=createDecisionDraft().suite;const csv='\uFEFFid,pair_id,task_id,language,text,expected\r\nc1,p1,,ko,"줄1\n줄2 ""인용""",D01\r\n';const imported=parseDecisionCSV(csv,s,s.tasks[0].id);assert.equal(imported.cases.length,1);assert.equal(imported.cases[0].text,'줄1\n줄2 "인용"');assert.equal(imported.cases[0].taskID,s.tasks[0].id);assert.equal(imported.source,'custom');
});
test('CSV failures include physical row numbers and leave the existing suite unchanged',()=>{
 const s=createDecisionDraft().suite;const original=JSON.stringify(s);const header='id,pair_id,task_id,language,text,expected\n';const good='c1,p1,'+s.tasks[0].id+',ko,질문,D01\n';
 for(const bad of ['c1,p2,'+s.tasks[0].id+',ko,질문,D01\n','c2,p1,'+s.tasks[0].id+',ko,질문,D01\n','c2,p2,missing,ko,질문,D01\n','c2,p2,'+s.tasks[0].id+',en,question,D01\n','c2,p2,'+s.tasks[0].id+',ko,질문,invalid\n'])assert.throws(()=>parseDecisionCSV(header+good+bad,s),/3행/);
 assert.throws(()=>parseDecisionCSV('id,text,expected\nc1,"열린 문장,D01',s,s.tasks[0].id),/2행/);assert.equal(JSON.stringify(s),original);
});

test('experiment name survives draft storage without inventing execution hardware',()=>{
 const draft=createDecisionDraft();draft.experimentName='업무영역 tev1 비교';const restored=restoreDecisionDraft(JSON.stringify(draft));assert.equal(restored.experimentName,draft.experimentName);assert.ok(draft.targets.every(t=>t.environment===''));
 assert.throws(()=>restoreDecisionDraft(JSON.stringify({...draft,experimentName:{name:'bad'}})),/설정 초안/);
});

test('legacy default drafts are refreshed without the old brand in displayed metadata',()=>{
 const draft=createDecisionDraft();draft.suite.id='mybee-domain-pilot';draft.suite.name='MyBee 업무영역';draft.suite.sourceDocument='MyBee_설계서';draft.experimentName='MyBee 비교';
 const loaded=restoreDecisionDraft(JSON.stringify(draft));assert.equal(loaded.suite.id,'business-domain-pilot');assert.equal(/mybee/i.test(loaded.suite.name+' '+loaded.suite.sourceDocument+' '+loaded.experimentName),false);
});

test('compact and detailed profiles keep answer IDs while using distinct input criteria',()=>{
 const compact=createDecisionSuite();const detailed=createDecisionSuite('detailed');
 assert.equal(compact.questionStyle,'compact');assert.equal(detailed.questionStyle,'detailed');
 assert.notEqual(compact.revision,detailed.revision);
 assert.deepEqual(compact.cases.map(c=>[c.id,c.expected]),detailed.cases.map(c=>[c.id,c.expected]));
 assert.deepEqual(validateDecisionSuite(compact),[]);assert.deepEqual(validateDecisionSuite(detailed),[]);
 assert.ok(compact.tasks[0].instructions.ko.length<50);
 assert.ok(compact.tasks[0].options.every(o=>o.descriptions.ko.length<10));
 for(const c of compact.cases.filter(c=>c.candidate)){
  assert.ok(c.instructions.ko.length>10);
  assert.equal(c.text.includes('후보 기준:'),false);
  const preview=buildSystemOnePreview(compact,c,'model');
  assert.equal('expected' in preview.state,false);assert.equal('rationale' in preview.state,false);
 }
});
test('old built-in drafts adopt compact input while explicit detailed and custom drafts survive',()=>{
 const draft=createDecisionDraft();draft.suite=createDecisionSuite('detailed');
 assert.equal(restoreDecisionDraft(JSON.stringify(draft)).suite.questionStyle,'detailed');
 delete draft.suite.questionStyle;draft.suite.revision='0.2.0-draft';
 assert.equal(restoreDecisionDraft(JSON.stringify(draft)).suite.questionStyle,'compact');
 draft.suite.source='custom';
 assert.deepEqual(restoreDecisionDraft(JSON.stringify(draft)).suite,draft.suite);
});

test('definitions and candidate questions stay outside the state while imports preserve per-case questions',()=>{
 const draft=createDecisionDraft();assert.equal(draft.suite.tasks[0].context,undefined);
 for(const c of draft.suite.cases){
  const task=draft.suite.tasks.find(t=>t.id===c.taskID);const request=buildSystemOnePreview(draft.suite,c,'model');
  assert.equal(request.state.body,c.text);assert.equal(c.text.includes('후보 기준:'),false);assert.equal(c.text.includes('후보 업무영역:'),false);
  assert.equal(request.questions.decision.instructions,c.instructions?.ko || task.instructions.ko);
  assert.equal(JSON.stringify(request).includes(c.rationale),false);
 }
 const imported=parseDecisionSuite(JSON.stringify(draft.suite));assert.equal(imported.cases[20].expected,draft.suite.cases[20].expected);
 assert.throws(()=>parseDecisionSuite(JSON.stringify({...draft.suite,cases:[{...draft.suite.cases[0],instructions:'invalid'}]})),/형식/);
 draft.suite.revision='0.4.0-draft-compact';draft.suite.tasks[0].context={ko:'legacy definitions',en:''};
 const loaded=restoreDecisionDraft(JSON.stringify(draft));assert.equal(loaded.suite.tasks[0].context,undefined);assert.equal(loaded.suite.revision,'0.7.0-draft-compact');
 const csv='id,task_id,text,expected,instructions\nc1,'+loaded.suite.tasks[1].id+',요청,D01|D02,회로 설계 요청인가요?\n';
 assert.equal(parseDecisionCSV(csv,loaded.suite).cases[0].instructions.ko,'회로 설계 요청인가요?');
});
