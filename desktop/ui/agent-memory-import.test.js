const {test}=require('node:test');const assert=require('node:assert/strict');const {run}=require('./agent-memory-import.js');
const drive=async replies=>{const notes=[];await run({invoke:async(_,{dryRun})=>replies[dryRun?0:1],ask:async()=>true,notice:t=>notes.push(t)});return notes;};
test('a null dry run shows the read error instead of throwing',async()=>{
 assert.deepEqual(await drive([null]),['기억 파일을 읽지 못했어요.']);
});
test('a null import reply shows the retry error instead of throwing',async()=>{
 assert.deepEqual(await drive([{statements:2,files:1},null]),['가져오는 중이에요.','가져오지 못했어요. 잠시 뒤 다시 시도해 주세요.']);
});
test('a normal import reports the added and failed counts',async()=>{
 assert.deepEqual(await drive([{statements:3,files:2},{candidates:2,failed:1}]),['가져오는 중이에요.','검토 대기에 2개를 올렸어요. 1개는 실패했어요.']);
});
