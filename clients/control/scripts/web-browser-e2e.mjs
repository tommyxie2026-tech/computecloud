import { chromium, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const [baseURL, specFile, outputDir, mode = 'primary'] = process.argv.slice(2);
if (!baseURL || !specFile || !outputDir) throw new Error('base URL, spec file and output directory required');
fs.mkdirSync(outputDir,{recursive:true});
const browser = await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ? {executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE} : {})});
const context = await browser.newContext(mode==='rotated' ? {storageState:path.join(path.dirname(specFile),'old-web-session.json')} : {});
const page = await context.newPage();
const errors=[];page.on('pageerror',e=>errors.push(e.message));
const checks=[];const failures=[];
page.on('response',async r=>{if(r.status()>=400){let code='';try{code=(await r.json()).error?.code||''}catch{};failures.push({path:new URL(r.url()).pathname,status:r.status(),code})}});
const streams=[];const activeStreams=new Set();
page.on('request',r=>{if(new URL(r.url()).pathname.endsWith('/events/stream')){streams.push(r.url());activeStreams.add(r)}});
page.on('requestfinished',r=>activeStreams.delete(r));page.on('requestfailed',r=>activeStreams.delete(r));
const login = async(password) => {await page.getByLabel('用户名',{exact:true}).fill('admin');await page.getByLabel('密码',{exact:true}).fill(password);await page.getByRole('button',{name:'登录',exact:true}).click()};
try {
  await page.goto(baseURL+'/ui/');
  await expect(page.getByRole('button',{name:'登录',exact:true})).toBeVisible();
  await login(mode==='rotated' ? 'admin' : 'incorrect-fixture-password');await expect(page.getByRole('alert')).toContainText('用户名或密码错误');checks.push('invalid_password');
  await login(process.env.TEST_WEB_PASSWORD || 'admin');await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await expect(page.getByText('worker-control',{exact:true})).toBeVisible({timeout:15000});checks.push('login_and_workers');
  await page.reload();await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();checks.push('refresh_session');
  if (mode==='primary') {
    const spec=JSON.parse(fs.readFileSync(specFile,'utf8'));
    const submit=async(value)=>{await page.getByLabel('任务 JSON').fill(JSON.stringify(value));await page.getByRole('button',{name:'提交任务',exact:true}).click()};
    const created=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/v1/jobs');
    await submit(spec);const response=await created;expect(response.status()).toBe(202);const job=await response.json();
    await expect(page.getByTestId('selected-job-state')).toHaveText('SUCCEEDED',{timeout:45000});checks.push('submit_to_terminal');
    const replay=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/v1/jobs');await page.getByRole('button',{name:'提交任务',exact:true}).click();const repeated=await replay;expect(repeated.status()).toBe(200);expect((await repeated.json()).job_id).toBe(job.job_id);checks.push('duplicate_submit_idempotent');
    await expect(page.getByText('执行产物',{exact:true})).toBeVisible();checks.push('artifact_section');
    await page.getByRole('button',{name:'新建提交标识'}).click();spec.input={text:'hold-control'};await submit(spec);await expect(page.getByRole('button',{name:'获取控制权'})).toBeEnabled({timeout:15000});
    await expect(page.getByTestId('selected-job-state')).toHaveText('EXECUTING',{timeout:15000});
    await page.reload();await expect(page.getByRole('button',{name:'获取控制权'})).toBeVisible();expect(new URL(page.url()).hash).toMatch(/^#job=/);checks.push('restore_selected_job');
    await expect.poll(()=>activeStreams.size).toBeGreaterThan(0);
    const streamCount=streams.length;
    await context.setOffline(true);await new Promise(r=>setTimeout(r,1500));await context.setOffline(false);
    await expect.poll(()=>streams.length,{timeout:15000}).toBeGreaterThan(streamCount);
    expect(Number(new URL(streams.at(-1)).searchParams.get('after_seq'))).toBeGreaterThan(0);
    checks.push('offline_observation_restore');
    await page.getByRole('button',{name:'获取控制权'}).click();await expect(page.getByRole('button',{name:'取消任务',exact:true})).toBeVisible();page.once('dialog',d=>d.accept());await page.getByRole('button',{name:'取消任务',exact:true}).click();await expect(page.getByTestId('selected-job-state')).toHaveText('CANCELED',{timeout:20000});checks.push('cancel_with_lease');
    await page.getByRole('button',{name:'新建提交标识'}).click();spec.input={text:'fail-verification-control'};spec.limits.max_attempts_per_task=2;spec.execution.replay_safe=true;
    await submit(spec);await expect(page.getByTestId('selected-job-state')).toHaveText('FAILED',{timeout:45000});
    await page.getByRole('button',{name:'获取控制权'}).click();await expect(page.getByRole('button',{name:'重试失败任务'})).toBeVisible();page.once('dialog',d=>d.accept());await page.getByRole('button',{name:'重试失败任务'}).click();await expect(page.getByTestId('selected-job-state')).toHaveText('SUCCEEDED',{timeout:45000});checks.push('bounded_manual_retry');
    await page.setViewportSize({width:390,height:844});await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);checks.push('narrow_view');
  }
  if(mode==='primary') {
    const activeSpec=JSON.parse(fs.readFileSync(specFile,'utf8'));activeSpec.input={text:'hold-control'};
    await page.getByRole('button',{name:'新建提交标识'}).click();await page.getByLabel('任务 JSON').fill(JSON.stringify(activeSpec));await page.getByRole('button',{name:'提交任务',exact:true}).click();
    await expect.poll(()=>activeStreams.size,{timeout:15000}).toBeGreaterThan(0);
  }
  const session = await page.evaluate(async()=>await (await fetch('/v1/web/auth/session')).json());
  await context.request.post(baseURL+'/v1/web/auth/logout',{headers:{Origin:baseURL,'X-CSRF-Token':session.csrf_token}});
  if(mode==='rotated') await page.reload();
  await expect(page.getByRole('button',{name:'登录',exact:true})).toBeVisible({timeout:15000});
  await expect.poll(()=>activeStreams.size).toBe(0);checks.push('revoked_session');
  await login(process.env.TEST_WEB_PASSWORD || 'admin');await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await page.getByRole('button',{name:'退出登录'}).click();await expect(page.getByRole('button',{name:'登录',exact:true})).toBeVisible();checks.push('logout');
  if(mode==='primary'){await login('admin');await expect(page.getByRole('button',{name:'退出登录'})).toBeVisible();await context.storageState({path:path.join(path.dirname(specFile),'old-web-session.json')});fs.chmodSync(path.join(path.dirname(specFile),'old-web-session.json'),0o600)}
  expect(errors).toEqual([]);console.log(JSON.stringify({status:'PASSED',checks,real_model_calls:false}));
} catch(e) {fs.writeFileSync(outputDir+'/failure.json',JSON.stringify({checks,failures:failures.slice(-30),active_streams:activeStreams.size,error:String(e)},null,2));await page.screenshot({path:outputDir+'/failure.png',fullPage:true});throw e;}
finally {await browser.close();}
