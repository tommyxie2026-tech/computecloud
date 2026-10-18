import assert from 'node:assert/strict';
import fs from 'node:fs';import os from 'node:os';import path from 'node:path';import ts from 'typescript';import {pathToFileURL} from 'node:url';
import React from 'react';import {act,create} from 'react-test-renderer';
globalThis.IS_REACT_ACT_ENVIRONMENT=true;
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'mobile-strictmode-'));
const compile=(name,source)=>fs.writeFileSync(path.join(dir,name),ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.ES2022,target:ts.ScriptTarget.ES2022,jsx:ts.JsxEmit.React}}).outputText);
for(const name of ['api','readRecovery','webSnapshot'])compile(name+'.mjs',fs.readFileSync(new URL('../src/'+name+'.ts',import.meta.url),'utf8'));
const {ControlAPI,isSnapshotReset}=await import(pathToFileURL(path.join(dir,'api.mjs')));
const {readSnapshotWithRetry,subscribeConnectivity}=await import(pathToFileURL(path.join(dir,'readRecovery.mjs')));
const {mergeJobSnapshot,jobFromHash,jobHash}=await import(pathToFileURL(path.join(dir,'webSnapshot.mjs')));
globalThis.nativeTestServices={React,ControlAPI,isSnapshotReset,readSnapshotWithRetry,subscribeConnectivity,mergeJobSnapshot,jobFromHash,jobHash,isNativeMobile:()=>true,MobileGate:()=>null,WebGate:()=>null,StyleSheet:{create:value=>value},useWindowDimensions:()=>({width:390,height:844}),ActivityIndicator:'ActivityIndicator',Pressable:'Pressable',ScrollView:'ScrollView',Text:'Text',TextInput:'TextInput',View:'View'};
let source=ts.transpileModule(fs.readFileSync(new URL('../App.tsx',import.meta.url),'utf8').replace('function ControlApp(', 'export function ControlApp('),{compilerOptions:{module:ts.ModuleKind.ES2022,target:ts.ScriptTarget.ES2022,jsx:ts.JsxEmit.React}}).outputText;
source=source.replace(/^import "@expo\/metro-runtime";\n/m,'').replace(/^import React, (\{[^;]+\}) from "react";/m,'const React=globalThis.nativeTestServices.React;const $1=React;').replace(/^import (\{[^;]+\}) from "[^"]+";/gm,'const $1=globalThis.nativeTestServices;');
fs.writeFileSync(path.join(dir,'app.mjs'),source);
const {ControlApp}=await import(pathToFileURL(path.join(dir,'app.mjs')));
const calls=[];let firstResolve;const bootstrap={protocol_max:'test',server_epoch:'epoch'};
globalThis.fetch=async(url,init)=>{
  calls.push({url,signal:init.signal});
  if(url.endsWith('/bootstrap')){
    if(!firstResolve)return new Promise(resolve=>{firstResolve=()=>resolve(new Response(JSON.stringify(bootstrap)));});
    return new Response(JSON.stringify(bootstrap));
  }
  return new Response(JSON.stringify(url.includes('/workers')?{workers:[]}:{jobs:[]}));
};
let renderer;
await act(async()=>{renderer=create(React.createElement(React.StrictMode,null,React.createElement(ControlApp,{mobileSession:{serverURL:'https://example.com',token:'key',deviceID:'device'},onExpired(){},onLogout(){},mobileSelected:{current:''}})));});
assert.equal(calls.filter(call=>call.url.endsWith('/bootstrap')).length,2);
assert.equal(calls[0].signal.aborted,true);
assert.equal(calls.filter(call=>call.url.includes('/jobs?')).length,1);
const count=calls.length;
await act(async()=>{firstResolve();await Promise.resolve();});
assert.equal(calls.length,count,'Stale bootstrap must not perform collection reads');
assert.equal(renderer.root.findAll(node=>node.type==='Text'&&node.children.includes('重试连接')).length,0,'Stale cleanup must not clear the new connection');
const activeSignal=calls.findLast(call=>call.url.endsWith('/bootstrap')).signal;
await act(async()=>renderer.unmount());assert.equal(activeSignal.aborted,true);
console.log('Real React StrictMode setup/cleanup/setup keeps the new connection and fences delayed bootstrap');
