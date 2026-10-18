// Component state/async boundary test with mocked native services; not a simulator test.
import assert from 'node:assert/strict';
import fs from 'node:fs'; import os from 'node:os'; import path from 'node:path'; import ts from 'typescript'; import {pathToFileURL} from 'node:url';
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'mobile-gate-'));
const compile=(file, source)=>{const output=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.ES2022,target:ts.ScriptTarget.ES2022,jsx:ts.JsxEmit.React}}).outputText;fs.writeFileSync(path.join(dir,file),output);};
compile('policy.mjs',fs.readFileSync(new URL('../src/mobilePolicy.ts',import.meta.url),'utf8'));
let slots=[], cursor=0, effects=[], tree, listener, linkListener, saved=[], deleted=0, requests=[], resolveRequest, blockSave=false, releaseSave;
const operations=[];
const react={createElement:(type,props,...children)=>({type,props:props||{},children}), useState(value){const i=cursor++;if(!(i in slots))slots[i]=typeof value==='function'?value():value;return [slots[i],value=>{slots[i]=typeof value==='function'?value(slots[i]):value;}];},useRef(value){const i=cursor++;return slots[i]??(slots[i]={current:value});},useEffect(fn){const i=cursor++;if(!(i in slots)){slots[i]=true;effects.push(fn);}}};
class APIError extends Error {status=401;}
globalThis.services={React:react, ...react,AppState:{currentState:'active',addEventListener(_,fn){listener=fn;return {remove(){}}}}, Button:'Button',Text:'Text',TextInput:'TextInput',ScrollView:'ScrollView',View:'View',ControlAPIError:APIError,
 ControlAPI:class{constructor(url,key){this.url=url;this.key=key;}bootstrap(signal){requests.push({url:this.url,key:this.key,signal});return new Promise(resolve=>resolveRequest=resolve);}},
 clearMobileProfile:async()=>{deleted++;operations.push("clear");},loadMobileProfile:async()=>null,loadOrCreateDeviceID:async()=>'device-test',saveMobileProfile:async(...values)=>{saved.push(values);operations.push("save");if(blockSave)await new Promise(resolve=>releaseSave=resolve);},initialConnectURL:async()=>null,serverFromConnectURL:url=>url==='safe-link'?'https://other.example':null,subscribeConnectURLs:fn=>{linkListener=fn;return ()=>{};}};
let source=fs.readFileSync(new URL('../src/MobileGate.tsx',import.meta.url),'utf8');
source=source.replace(/^import .* from 'react';$/m, 'const {React,useEffect,useRef,useState}=globalThis.services;').replace(/^import .* from 'react-native';$/m,'const {AppState,Button,ScrollView,Text,TextInput,View}=globalThis.services;').replace(/^import .* from '.\/api';$/m,'const {ControlAPI,ControlAPIError}=globalThis.services;').replace(/^import .* from '.\/mobile';$/m,'const {clearMobileProfile,initialConnectURL,loadMobileProfile,loadOrCreateDeviceID,saveMobileProfile,serverFromConnectURL,subscribeConnectURLs}=globalThis.services;').replace("'./mobilePolicy'","'./policy.mjs'");
compile('gate.mjs',source);const {MobileGate}=await import(pathToFileURL(path.join(dir,'gate.mjs')));
const render=()=>{cursor=0;tree=MobileGate({children:(session,expired,disconnect,selected,draft)=>({type:'Session',props:{session,expired,disconnect,selected,draft},children:[]})});const pending=effects;effects=[];pending.forEach(fn=>fn());return tree;};
const tick=async()=>{for(let i=0;i<12;i++)await Promise.resolve();render();};
const find=(predicate,node=tree)=>{if(node&&typeof node==='object'){if(predicate(node))return node;for(const child of node.children||[]){const found=find(predicate,child);if(found)return found;}}};
const input=label=>find(node=>node.props?.accessibilityLabel===label);
const connect=()=>find(node=>node.type==='Button'&&node.props.title==='连接').props.onPress();
render();await tick();
input('Server 地址').props.onChangeText('https://first.example');await tick();input('API key').props.onChangeText('first-key');await tick();connect();await tick();
assert.equal(requests[0].url,'https://first.example');
// A different endpoint arrives while bootstrap is pending: no stale profile/session commit.
linkListener('safe-link');await tick();assert.equal(requests[0].signal.aborted,true);assert.equal(input('API key').props.value,'');
resolveRequest({});await tick();assert.equal(saved.length,0);assert.equal(find(n=>n.type==='Session'),undefined);
input('API key').props.onChangeText('second-key');await tick();connect();resolveRequest({});await tick();
let sessionNode=find(n=>n.type==='Session');assert.equal(sessionNode.props.session.serverURL,'https://other.example');assert.equal(saved.length,1);
sessionNode.props.selected.current='job-1';sessionNode.props.draft.current={spec:'{draft}',key:'same-submit-key'};listener('background');await tick();assert.equal(find(n=>n.type==='Session'),undefined);
listener('active');await tick();sessionNode=find(n=>n.type==='Session');assert.equal(sessionNode.props.selected.current,'job-1');assert.equal(sessionNode.props.draft.current.key,'same-submit-key');
const staleExpiry=sessionNode.props.expired;sessionNode.props.disconnect();await tick();assert.equal(input('API key').props.value,'');
input('API key').props.onChangeText('third-key');await tick();connect();resolveRequest({});await tick();
staleExpiry();await tick();assert.equal(find(n=>n.type==='Session').props.session.token,'third-key');
find(n=>n.type==='Session').props.expired();await tick();assert.equal(find(n=>n.type==='Session'),undefined);assert.equal(input('API key').props.value,'');assert.ok(deleted>0);
console.log('MobileGate endpoint switch, stale bootstrap/401, Keychain, background and reconnect checks passed (native services mocked)');

blockSave=true;input('API key').props.onChangeText('slow-save-key');await tick();connect();resolveRequest({});await tick();
assert.equal(typeof releaseSave,'function');
find(n=>n.type==='Button'&&n.props.title==='清除保存的连接').props.onPress();await tick();releaseSave();await tick();
assert.equal(operations.at(-1),'clear');assert.equal(find(n=>n.type==='Session'),undefined);
console.log('Logout during pending Keychain save is serialized after save and cannot restore the old connection');
