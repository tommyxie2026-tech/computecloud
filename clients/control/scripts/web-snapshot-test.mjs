import assert from 'node:assert/strict';import fs from 'node:fs';import path from 'node:path';import os from 'node:os';import ts from 'typescript';import {pathToFileURL} from 'node:url';
const modulePath=path.join(fs.mkdtempSync(path.join(os.tmpdir(),'control-snapshot-')),'snapshot.mjs');
const source=fs.readFileSync(new URL('../src/webSnapshot.ts',import.meta.url),'utf8');fs.writeFileSync(modulePath,ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.ES2022,target:ts.ScriptTarget.ES2022}}).outputText);
const {mergeJobSnapshot,jobFromHash,jobHash}=await import(pathToFileURL(modulePath));
const make=(from,to,last=to)=>({job:{job_id:'j',last_seq:String(last)},events:Array.from({length:to-from+1},(_,i)=>({seq:String(i+from)}))});
const merged=mergeJobSnapshot(make(401,900),make(1,500,900));assert.equal(merged.events.length,500);assert.equal(merged.events.at(-1).seq,'900');assert.equal(merged.events[0].seq,'401');
assert.equal(mergeJobSnapshot(make(401,900),make(1,20)).events.at(-1).seq,'20');
assert.equal(jobFromHash(jobHash('test-job')),'test-job');assert.equal(jobFromHash('#job='),null);assert.equal(jobFromHash('#job='+ 'a'.repeat(200)),null);
console.log('Snapshot event window and task URL restore checks passed');
