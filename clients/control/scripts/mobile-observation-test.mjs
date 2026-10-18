// Exercise real ControlApp callback bodies with controlled asynchronous API boundaries.
import assert from 'node:assert/strict';import fs from 'node:fs';import os from 'node:os';import path from 'node:path';import ts from 'typescript';import {pathToFileURL} from 'node:url';
const app=fs.readFileSync(new URL('../App.tsx',import.meta.url),'utf8');
const directory=fs.mkdtempSync(path.join(os.tmpdir(),'mobile-observation-'));
async function callback(name, deps) {
  const start=app.indexOf(`  const ${name} = useCallback(async (`);assert.ok(start>=0);
  const argsStart=app.indexOf('async (',start);const end=app.indexOf('\n  }, [',argsStart);
  const expression=app.slice(argsStart,end)+'\n  }';
  const source=`export function create(deps) {const {${Object.keys(deps).join(',')}}=deps;return ${expression};}`;
  const file=path.join(directory,name+'.mjs');fs.writeFileSync(file,ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.ES2022,target:ts.ScriptTarget.ES2022}}).outputText);
  return (await import(pathToFileURL(file))).create(deps);
}
let release;let selectedWrites=0;let reads=0;
const streamAbort={current:new AbortController()},lifetime=new AbortController();
const selectedRef={current:''};
const load=await callback('loadJob', {api:{releaseWriteLease:()=>new Promise(resolve=>release=resolve)}, streamAbort,lifetime:{current:lifetime},writeLease:{lease_token:'lease'},selected:'old-job',holderID:{current:'device'},setWriteLease(){},setSelected(){selectedWrites++;},mobileSelected:selectedRef,webSession:null,setSnapshot(){},setBusy(){},setError(){},readSnapshotWithRetry(){reads++;throw Error('Must not start');},terminal:new Set()});
const pending=load('next-job');assert.equal(typeof release,'function');lifetime.abort();streamAbort.current.abort();release();await pending;
assert.equal(selectedRef.current,'');assert.equal(selectedWrites,0);assert.equal(reads,0);
let bootResolve;let collectionReads=0;const initLifetime=new AbortController();
const connect=await callback('connect',{streamAbort:{current:null},setBusy(){},setError(){},ControlAPI:class{bootstrap(){return new Promise(resolve=>bootResolve=resolve);}},webSession:null,mobileSession:{serverURL:'https://example.com',token:'key'},onExpired(){},lifetime:{current:initLifetime},refreshCollections(){collectionReads++;},setAPI(){},setBootstrap(){},setSelected(){},setSnapshot(){},setWriteLease(){}});
const init=connect();initLifetime.abort();bootResolve({server_epoch:'epoch'});await init;assert.equal(collectionReads,0);
console.log('Actual task-switch and bootstrap callbacks stop after unmount before further reads or shared-ref writes');
