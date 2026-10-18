import "@expo/metro-runtime";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  useWindowDimensions,
  View,
} from "react-native";

import { mergeJobSnapshot, jobFromHash, jobHash } from "./src/webSnapshot";
import { readSnapshotWithRetry, subscribeConnectivity } from "./src/readRecovery";
import { MobileGate, type MobileSession, type MobileDraft } from "./src/MobileGate";
import { WebGate } from "./src/Login";
import type { WebSession } from "./src/webAuth";
import { ControlAPI, isSnapshotReset } from "./src/api";
import { isNativeMobile } from "./src/mobile";
import type {
  ControlBootstrap,
  JobEvent,
  JobPage,
  JobSnapshot,
  JobSummary,
  WorkerPage,
  ControlWriteLease,
  SessionView,
  ApprovalView,
  ArtifactReview,
} from "./src/types";

const terminal = new Set(["SUCCEEDED", "FAILED", "CANCELED"]);

function operationID(prefix: string) {
  const uuid = globalThis.crypto?.randomUUID?.();
  return `${prefix}-${uuid ?? Date.now().toString(36)}`;
}

function Button({ title, onPress, disabled = false }: { title: string; onPress: () => void; disabled?: boolean }) {
  return (
    <Pressable accessibilityRole="button" disabled={disabled} onPress={onPress}
      style={({ pressed }) => [styles.button, disabled && styles.buttonDisabled, pressed && !disabled && styles.buttonPressed]}>
      <Text style={styles.buttonText}>{title}</Text>
    </Pressable>
  );
}

function StatePill({ value, testID }: { value: string; testID?: string }) {
  return <View testID={testID} style={styles.pill}><Text style={styles.pillText}>{value}</Text></View>;
}

function Empty({ children }: { children: React.ReactNode }) {
  return <Text style={styles.muted}>{children}</Text>;
}

function formatTime(ms?: string) {
  if (!ms) return "—";
  const value = Number(ms);
  return Number.isFinite(value) ? new Date(value).toLocaleString() : ms;
}

function Events({ items }: { items: JobEvent[] }) {
  return (
    <View style={styles.stack}>
      {items.slice(-30).reverse().map((event) => (
        <View key={event.seq} style={styles.event}>
          <Text style={styles.eventSeq}>#{event.seq}</Text>
          <Text style={styles.eventType}>{event.type}</Text>
          <Text style={styles.eventTime}>{formatTime(event.recorded_at_ms)}</Text>
        </View>
      ))}
      {!items.length && <Empty>暂无事件。</Empty>}
    </View>
  );
}

export default function App() {
  if (isNativeMobile()) return <MobileGate>{(session, expired, disconnect, selected, draft) => <ControlApp mobileSession={session} onExpired={expired} onLogout={disconnect} mobileSelected={selected} mobileDraft={draft} />}</MobileGate>;
  return <WebGate>{(session, expired, signOut) => <ControlApp key={session.csrf_token} webSession={session} onExpired={expired} onLogout={signOut} />}</WebGate>;
}
function ControlApp({ webSession, mobileSession, mobileSelected, mobileDraft, onExpired, onLogout }: { webSession?: WebSession; mobileSession?: MobileSession; mobileSelected?: React.MutableRefObject<string>; mobileDraft?: React.MutableRefObject<MobileDraft>; onExpired?: () => void; onLogout?: () => void }) {
  const { width } = useWindowDimensions();
  const compact = width < 960;
  const [api, setAPI] = useState<ControlAPI | null>(null);
  const [bootstrap, setBootstrap] = useState<ControlBootstrap | null>(null);
  const [jobPage, setJobPage] = useState<JobPage | null>(null);
  const [workers, setWorkers] = useState<WorkerPage | null>(null);
  const [selected, setSelected] = useState<string>("");
  const [snapshot, setSnapshot] = useState<JobSnapshot | null>(null);
  const [artifactPreview, setArtifactPreview] = useState<{ client: ControlAPI; jobID: string; review: ArtifactReview } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [writeLease, setWriteLease] = useState<ControlWriteLease | null>(null);
  const [sessionInput, setSessionInput] = useState("");
  const [newJobSpec, setNewJobSpec] = useState(mobileDraft?.current.spec ?? "{}");
  const [newJobKey, setNewJobKey] = useState(() => mobileDraft?.current.key || operationID("job"));
  useEffect(() => { if (mobileDraft) mobileDraft.current = {spec: newJobSpec, key: newJobKey}; }, [mobileDraft, newJobSpec, newJobKey]);
  const lifetime = useRef(new AbortController());
  const streamAbort = useRef<AbortController | null>(null);
  const holderID = useRef(operationID("device"));

  useEffect(() => { if (mobileSession) holderID.current = mobileSession.deviceID; }, [mobileSession]);

  const attention = useMemo(() => {
    if (!snapshot) return 0;
    return snapshot.approvals.filter((item) => item.state === "PENDING").length +
      snapshot.sessions.filter((item) => item.state === "WAITING_INPUT" || item.state === "WAITING_APPROVAL" || item.state === "UNVERIFIABLE").length;
  }, [snapshot]);

  const refreshCollections = useCallback(async (client: ControlAPI, epoch?: string) => {
    const q = new URLSearchParams({ limit: "25" });
    const w = new URLSearchParams({ limit: "25" });
    if (epoch) {
      q.set("epoch", epoch);
      w.set("epoch", epoch);
    }
    const [jobs, workerPage] = await Promise.all([client.listJobs(q), client.listWorkers(w)]);
    if (client.closed) return;
    setJobPage(jobs);
    setWorkers(workerPage);
  }, []);

  const connect = useCallback(async () => {
    const connection = lifetime.current;
    streamAbort.current?.abort();
    setBusy(true);
    setError("");
    try {
      const client = new ControlAPI(webSession ? globalThis.location.origin : mobileSession!.serverURL, webSession ? { kind: "web", csrfToken: webSession.csrf_token, onExpired: onExpired! } : { kind: "native", token: mobileSession!.token, onExpired: onExpired! }, connection.signal);
      const boot = await client.bootstrap();
      if (connection.signal.aborted) return;
      await refreshCollections(client, boot.server_epoch);
      if (connection.signal.aborted) return;
      setAPI(client);
      setBootstrap(boot);
      setSelected("");
      setSnapshot(null);
      setWriteLease(null);

    } catch (e) {
      if (connection.signal.aborted) return;
      setAPI(null);
      setBootstrap(null);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!connection.signal.aborted) setBusy(false);
    }
  }, [refreshCollections, webSession?.csrf_token, mobileSession]);
  useEffect(() => {
    const connection = new AbortController();
    lifetime.current = connection;
    if (webSession || mobileSession) void connect();
    return () => { connection.abort(); streamAbort.current?.abort(); };
  }, [webSession?.csrf_token, mobileSession]);

  const resetSnapshot = useCallback(async () => {
    if (!api) return;
    const boot = await api.bootstrap();
    setBootstrap(boot);
    await refreshCollections(api, boot.server_epoch);
  }, [api, refreshCollections]);

  const loadMoreJobs = useCallback(async () => {
    if (!api || !jobPage?.has_more || !jobPage.next) return;
    setBusy(true);
    try {
      const q = new URLSearchParams({
        limit: "25",
        epoch: jobPage.server_epoch,
        snapshot_ms: jobPage.snapshot_ms,
        snapshot_id: jobPage.snapshot_id,
        before_created_ms: jobPage.next.created_at_ms,
        before_id: jobPage.next.job_id,
      });
      const next = await api.listJobs(q);
      setJobPage({ ...next, jobs: [...jobPage.jobs, ...next.jobs] });
    } catch (e) {
      if (isSnapshotReset(e)) await resetSnapshot();
      else setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, jobPage, resetSnapshot]);

  const loadJob = useCallback(async (jobID: string) => {
    const connection = lifetime.current;
    if (!api) return;
    streamAbort.current?.abort();
    const controller = new AbortController();
    streamAbort.current = controller;
    if (connection.signal.aborted) { controller.abort(); return; }
    if (writeLease && selected) {
      try { await api.releaseWriteLease(selected, holderID.current, writeLease.lease_token); } catch { /* lease expires quickly */ }
      if (controller.signal.aborted || connection.signal.aborted) return;
      setWriteLease(null);
    }
    setSelected(jobID);
    if (mobileSelected) mobileSelected.current = jobID;
    if (webSession) globalThis.history.replaceState(null, "", globalThis.location.pathname + globalThis.location.search + jobHash(jobID));
    setSnapshot(null);
    setBusy(true);
    setError("");
    try {
      const initial = await readSnapshotWithRetry(api, jobID, controller.signal, () => { if (!controller.signal.aborted) { setBusy(false); setError("连接暂时中断，正在重新获取任务状态…"); } });
      if (controller.signal.aborted) return;
      setError("");
      setSnapshot(initial);
      setBusy(false);
      let cursor = initial.events.at(-1)?.seq ?? "0";
      if (terminal.has(initial.job.state)) return;
      let retryMS = 1000;
      while (!controller.signal.aborted) {
        try {
          await api.streamEvents(jobID, cursor, controller.signal, (event) => {
            cursor = event.seq;
            setSnapshot((current) => current ? { ...current, events: current.events.some(item => item.seq === event.seq) ? current.events : [...current.events.slice(-499), event] } : current);
          });
          retryMS = 1000;
        } catch (e) {
          if (controller.signal.aborted) return;
          if (e instanceof Error && e.name === "ControlAPIError" && "status" in e && e.status === 401) return;
          if (isSnapshotReset(e) || (e instanceof Error && e.message === "EVENT_CURSOR_EXPIRED")) {
            const reset = await api.jobSnapshot(jobID); setSnapshot(reset); cursor = reset.events.at(-1)?.seq ?? "0";
          }
        }
        if (controller.signal.aborted) return;
        try {
          const latest = await readSnapshotWithRetry(api, jobID, controller.signal, () => { if (!controller.signal.aborted) setError("连接暂时中断，正在恢复任务观察…"); });
          setError("");
          if (controller.signal.aborted) return;
          setSnapshot(current => mergeJobSnapshot(current, latest));
          if (terminal.has(latest.job.state)) return;
        } catch { if (controller.signal.aborted) return; }
        await new Promise<void>(resolve => { const stop = () => { clearTimeout(timer); resolve(); }; const timer = setTimeout(() => { controller.signal.removeEventListener("abort", stop); resolve(); }, retryMS); controller.signal.addEventListener("abort", stop, { once: true }); });
        retryMS = Math.min(retryMS * 2, 10000);
      }
    } catch (e) {
      if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }, [api, selected, writeLease]);

  useEffect(() => {
    if (mobileSession && api && mobileSelected?.current) void loadJob(mobileSelected.current);
  }, [api, mobileSession]);

  useEffect(() => {
    if (!webSession || !api) return;
    const restore = () => { const id = jobFromHash(globalThis.location.hash); if (id) void loadJob(id); };
    restore(); globalThis.addEventListener("hashchange", restore);
    return () => globalThis.removeEventListener("hashchange", restore);
  }, [api]);

  useEffect(() => {
    if (!webSession || !api) return;
    return subscribeConnectivity(globalThis.window, () => {
      streamAbort.current?.abort();
      setWriteLease(null);
      setBusy(false);
      setError("网络已断开，任务仍由 Server 执行；恢复后将重新同步。");
    }, () => {
      setError("");
      if (selected) void loadJob(selected);
      else void refreshCollections(api, bootstrap?.server_epoch).catch(e => setError(e instanceof Error ? e.message : String(e)));
    });
  }, [api, selected, webSession?.csrf_token]);


  useEffect(() => {
    if (!api || !selected || !writeLease) return;
    const token = writeLease.lease_token;
    const timer = setInterval(async () => {
      try {
        const renewed = await api.renewWriteLease(selected, holderID.current, token);
        setWriteLease(renewed);
      } catch (e) {
        setWriteLease(null);
        setError(e instanceof Error ? e.message : String(e));
      }
    }, 10000);
    return () => clearInterval(timer);
  }, [api, selected, writeLease?.lease_token]);

  const takeControl = useCallback(async () => {
    if (!api || !selected) return;
    setBusy(true);
    setError("");
    try {
      setWriteLease(await api.acquireWriteLease(selected, holderID.current));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, selected]);

  const releaseControl = useCallback(async () => {
    if (!api || !selected || !writeLease) return;
    const current = writeLease;
    setWriteLease(null);
    try {
      await api.releaseWriteLease(selected, holderID.current, current.lease_token);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [api, selected, writeLease]);

  const refreshSelected = useCallback(async () => {
    if (!api || !selected) return;
    setSnapshot(await api.jobSnapshot(selected));
  }, [api, selected]);

  useEffect(() => {
    if (!api || !selected || !snapshot || terminal.has(snapshot.job.state)) return;
    let active = true; let pending = false;
    const timer = setInterval(async () => {
      if (pending) return; pending = true;
      try { const latest = await api.jobSnapshot(selected); if (active) setSnapshot(current => mergeJobSnapshot(current, latest)); }
      catch { /* event reconnection reports persistent failures */ }
      finally { pending = false; }
    }, 3000);
    return () => { active = false; clearInterval(timer); };
  }, [api, selected, snapshot?.job.state]);

  const submitNewJob = useCallback(async () => {
    if (!api) return;
    setBusy(true);
    setError("");
    try {
      const spec = JSON.parse(newJobSpec) as unknown;
      const created = await api.submitJob(spec, newJobKey);
      const boot = bootstrap ?? await api.bootstrap();
      if (!bootstrap) setBootstrap(boot);
      await refreshCollections(api, boot.server_epoch);
      await loadJob(created.job_id);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, newJobSpec, newJobKey, bootstrap, refreshCollections, loadJob]);


  const cancelSelected = useCallback(async () => {
    if (!api || !selected || !writeLease) return;
    if (webSession && !globalThis.confirm(`确认取消任务 ${selected}？`)) return;
    setBusy(true);
    try {
      await api.cancelJob(selected, writeLease.lease_token, operationID("cancel"), "Canceled from Control client");
      await refreshSelected();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, selected, writeLease, refreshSelected]);

  const retryTask = useCallback(async (task: JobSnapshot["tasks"][number]) => {
    if (!api || !selected || !writeLease) return;
    setBusy(true);
    setError("");
    try {
      if (webSession && !globalThis.confirm(`确认重试任务 ${task.task_id}？`)) return;
      await api.manualRetry(selected, task, writeLease.lease_token, operationID("retry"));
      await refreshSelected();
      if (bootstrap) await refreshCollections(api, bootstrap.server_epoch);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, selected, writeLease, refreshSelected, refreshCollections, bootstrap]);

  const sendSessionInput = useCallback(async (session: SessionView) => {
    if (!api || !selected || !writeLease || !snapshot || !sessionInput.trim()) return;
    setBusy(true);
    try {
      await api.sendInput(selected, session, writeLease.lease_token, operationID("input"), Number(snapshot.job.version), sessionInput.trim());
      setSessionInput("");
      await refreshSelected();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, selected, writeLease, snapshot, sessionInput, refreshSelected]);

  const resumeSession = useCallback(async (session: SessionView) => {
    if (!api || !selected || !writeLease || !snapshot) return;
    setBusy(true);
    try {
      await api.resumeSession(selected, session, writeLease.lease_token, operationID("resume"), Number(snapshot.job.version));
      await refreshSelected();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, selected, writeLease, snapshot, refreshSelected]);

  const decideApproval = useCallback(async (approval: ApprovalView, decision: "ACCEPT" | "REJECT") => {
    if (!api || !selected || !writeLease || !snapshot) return;
    setBusy(true);
    try {
      await api.decideApproval(selected, approval, writeLease.lease_token, operationID("approval"), Number(snapshot.job.version), decision);
      await refreshSelected();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [api, selected, writeLease, snapshot, refreshSelected]);

  const openArtifact = useCallback(async (artifactID: string) => {
    if (!api || !selected) return;
    setBusy(true);
    setError("");
    setArtifactPreview(null);
    try {
      const review = await api.artifactReview(selected, artifactID);
      setArtifactPreview({ client: api, jobID: selected, review });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Artifact review unavailable");
    } finally { setBusy(false); }
  }, [api, selected]);

  return (
    <View style={styles.root}>
      <View style={styles.header}>
        <View>
          <Text style={styles.eyebrow}>AGENT JOB EXECUTOR</Text>
          <Text style={styles.title}>computecloud Control</Text>
        </View>
        <View style={styles.headerMeta}>
          {(webSession || mobileSession) && !api && <Button title="重试连接" onPress={connect} disabled={busy} />}
          {(webSession || mobileSession) && <Button title={webSession ? "退出登录" : "切换 Server / 退出连接"} onPress={onLogout!} />}
          {bootstrap ? <StatePill value={`ACP ${bootstrap.protocol_max}`} /> : <StatePill value="未连接" />}
          {!!attention && <StatePill value={`${attention} attention`} />}
        </View>
      </View>

      {!!error && <View style={styles.errorBox}><Text style={styles.errorText}>{error}</Text></View>}

      <View style={[styles.body, compact && styles.bodyCompact]}>
        <View style={[styles.sidebar, compact && styles.sidebarCompact]}>
          <Text style={styles.sectionTitle}>新建任务</Text>
          <Text style={styles.muted}>填写任务 JSON。Server 将校验项目、运行时、凭据、模板与策略。</Text>
          <TextInput
            accessibilityLabel="任务 JSON"
            multiline
            value={newJobSpec}
            onChangeText={setNewJobSpec}
            style={[styles.input, styles.specInput]}
            placeholder='{"schema_version":"v0.2", ...}'
            placeholderTextColor="#66717f"
          />
          <Text style={styles.mono}>Idempotency-Key {newJobKey}</Text>
          <View style={styles.actionRow}>
            <Button title="提交任务" onPress={submitNewJob} disabled={busy || !api} />
            <Button title="新建提交标识" onPress={() => setNewJobKey(operationID("job"))} disabled={busy} />
          </View>

          <Text style={[styles.sectionTitle, styles.topGap]}>任务列表</Text>
          <ScrollView style={styles.list}>
            {jobPage?.jobs.map((job: JobSummary) => (
              <Pressable key={job.job_id} onPress={() => loadJob(job.job_id)}
                style={[styles.jobRow, selected === job.job_id && styles.jobRowSelected]}>
                <View style={styles.rowBetween}>
                  <Text numberOfLines={1} style={styles.jobID}>{job.job_id}</Text>
                  <StatePill value={job.state} />
                </View>
                <Text style={styles.muted}>{job.project} · {job.mode}</Text>
                <Text style={styles.muted}>{formatTime(job.updated_at_ms)}</Text>
              </Pressable>
            ))}
            {!jobPage?.jobs.length && <Empty>暂无任务，请提交任务或刷新列表。</Empty>}
          </ScrollView>
          {jobPage?.has_more && <Button title="加载更多" onPress={loadMoreJobs} disabled={busy} />}

          <Text style={[styles.sectionTitle, styles.topGap]}>Worker 节点</Text>
          <ScrollView style={styles.workerList}>
            {workers?.workers.map((worker) => (
              <View key={worker.worker_id} style={styles.workerRow}>
                <View style={styles.rowBetween}>
                  <Text style={styles.jobID}>{worker.worker_id}</Text>
                  <StatePill value={worker.online ? "在线" : "离线"} />
                </View>
                <Text style={styles.muted}>{worker.active}/{worker.slots} active</Text>
                {worker.runtimes.map((runtime) => (
                  <Text key={runtime.profile} style={styles.runtime}>{runtime.profile} {runtime.version ?? ""}</Text>
                ))}
              </View>
            ))}
          </ScrollView>
        </View>

        <ScrollView style={styles.detail} contentContainerStyle={styles.detailContent}>
          {!selected && <View style={styles.hero}><Text style={styles.heroTitle}>管理 Agent 任务执行</Text><Text style={styles.heroText}>提交新任务，或选择已有任务，查看执行节点、尝试记录、会话、审批、产物与实时事件。</Text></View>}
          {busy && selected && !snapshot && <ActivityIndicator />}
          {snapshot && (
            <>
              <View style={styles.panel}>
                <View style={styles.rowBetween}>
                  <View>
                    <Text style={styles.eyebrow}>JOB</Text>
                    <Text style={styles.detailTitle}>{snapshot.job.job_id}</Text>
                  </View>
                  <StatePill value={snapshot.job.state} testID="selected-job-state" />
                </View>
                <Text style={styles.muted}>mode {snapshot.job.mode} · seq {snapshot.job.last_seq} · updated {formatTime(snapshot.job.updated_at_ms)}</Text>
                {!!snapshot.job.error_code && <Text style={styles.errorText}>{snapshot.job.error_code}</Text>}
                <View style={styles.actionRow}>
                  {writeLease
                    ? <Button title="释放控制权" onPress={releaseControl} disabled={busy} />
                    : <Button title="获取控制权" onPress={takeControl} disabled={busy || bootstrap?.read_only || (terminal.has(snapshot.job.state) && snapshot.job.state !== "FAILED")} />}
                  {writeLease && !terminal.has(snapshot.job.state) && <Button title="取消任务" onPress={cancelSelected} disabled={busy} />}
                  {writeLease && <StatePill value="WRITE LEASE" />}
                </View>
              </View>

              <View style={[styles.grid, compact && styles.gridCompact]}>
                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>任务 / 执行尝试</Text>
                  {snapshot.tasks.map((task) => (
                    <View key={task.task_id} style={styles.item}>
                      <View style={styles.rowBetween}><Text style={styles.jobID}>{task.stage || "single"} · {task.partition_key}</Text><StatePill value={task.state} /></View>
                      <Text style={styles.mono}>task {task.task_id}</Text>
                      <Text style={styles.mono}>attempt {task.attempt_id || "—"} · gen {task.generation || "0"}</Text>
                      <Text style={styles.muted}>{task.worker_id || "尚未分配"} {task.scheduling_blocker ? `· ${task.scheduling_blocker}` : ""}</Text>
                      {!!task.error_code && <Text style={styles.errorText}>{task.error_code}</Text>}
                      {writeLease && snapshot.job.mode === "single" && snapshot.job.state === "FAILED" && task.state === "FAILED" && !!task.attempt_id && (
                        <View style={styles.controlStack}>
                          <Button title="重试失败任务" onPress={() => retryTask(task)} disabled={busy} />
                          <Text style={styles.muted}>Server 将重新校验重放安全、清理证明、截止期限、尝试预算与执行代次。</Text>
                        </View>
                      )}
                    </View>
                  ))}
                </View>

                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>运行会话</Text>
                  {snapshot.sessions.map((session) => (
                    <View key={session.session_id} style={styles.item}>
                      <View style={styles.rowBetween}><Text style={styles.jobID}>{session.runtime}</Text><StatePill value={session.state} /></View>
                      <Text style={styles.muted}>{session.runtime_version ?? "版本未知"} · gen {session.generation}</Text>
                      <Text style={styles.capabilities}>{session.capabilities.join(" · ") || "暂无交互能力"}</Text>
                      {writeLease && session.capabilities.includes("interactive_input") && (
                        <View style={styles.controlStack}>
                          <TextInput value={sessionInput} onChangeText={setSessionInput} style={styles.input} placeholder="向当前会话发送输入" placeholderTextColor="#66717f" />
                          <Button title="发送输入" onPress={() => sendSessionInput(session)} disabled={busy || !sessionInput.trim()} />
                        </View>
                      )}
                      {writeLease && session.capabilities.includes("session_resume") && (
                        <View style={styles.controlStack}><Button title="恢复会话" onPress={() => resumeSession(session)} disabled={busy} /></View>
                      )}
                    </View>
                  ))}
                  {!snapshot.sessions.length && <Empty>暂无会话。</Empty>}
                </View>

                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>待处理 / 审批</Text>
                  {snapshot.approvals.map((approval) => (
                    <View key={approval.approval_id} style={styles.item}>
                      <View style={styles.rowBetween}><Text style={styles.jobID}>{approval.tool}</Text><StatePill value={approval.state} /></View>
                      <Text>{approval.action}</Text>
                      <Text style={styles.muted}>{approval.risk_class} · request v{approval.request_version}</Text>
                      {!!approval.arguments_summary && <Text style={styles.mono}>{approval.arguments_summary}</Text>}
                      {writeLease && approval.state === "PENDING" && (
                        <View style={styles.actionRow}>
                          <Button title="批准" onPress={() => decideApproval(approval, "ACCEPT")} disabled={busy} />
                          <Button title="拒绝" onPress={() => decideApproval(approval, "REJECT")} disabled={busy} />
                        </View>
                      )}
                    </View>
                  ))}
                  {!snapshot.approvals.length && <Empty>暂无审批记录。</Empty>}
                </View>

                {snapshot.goal && <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>Goal review</Text>
                  <StatePill value={snapshot.goal.state} />
                  <Text style={styles.muted}>Attempts used: {snapshot.goal.consumed_attempts} / {snapshot.goal.max_total_attempts}</Text>
                  <Text style={styles.muted}>Plan {snapshot.goal.plan_revision} · execution graph {snapshot.goal.graph_generation}</Text>
                  <Text style={styles.muted}>{snapshot.goal.automatic_replan_enabled ? "Automatic re-planning enabled" : "Automatic re-planning is disabled"}</Text>
                  {snapshot.goal.governance && <>
                    <Text style={styles.muted}>{snapshot.goal.governance.usage_complete ? "Usage records complete" : "Usage is incomplete; unknown consumption is not zero"}</Text>
                    {snapshot.goal.governance.recent_decisions.map((decision) => <View key={decision.operation_id} style={styles.item}>
                      <Text style={styles.jobID}>{decision.action} · {decision.actor}</Text>
                      <Text>{decision.reason}</Text>
                      <Text style={styles.muted}>{formatTime(String(decision.created_at_ms))}</Text>
                    </View>)}
                    {!snapshot.goal.governance.recent_decisions.length && <Empty>No Goal governance decisions.</Empty>}
                  </>}
                </View>}

                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>执行产物</Text>
                  {snapshot.artifacts.map((artifact) => (
                    <Pressable key={artifact.artifact_id} onPress={() => openArtifact(artifact.artifact_id)} style={styles.item}>
                      <Text style={styles.jobID}>{artifact.kind}</Text>
                      <Text style={styles.mono}>{artifact.artifact_id}</Text>
                      <Text style={styles.muted}>{artifact.size} bytes · sha256 {artifact.sha256.slice(0, 12)}…</Text>
                    </Pressable>
                  ))}
                  {!snapshot.artifacts.length && <Empty>暂无已接收产物。</Empty>}
                  {artifactPreview?.client === api && artifactPreview?.jobID === selected && <View style={styles.stack}>
                    <Text style={styles.sectionTitle}>产物预览</Text>
                    <Text style={styles.mono}>Attempt {artifactPreview.review.attempt_id} · generation {artifactPreview.review.generation}</Text>
                    <Text style={styles.muted}>{artifactPreview.review.current_generation ? "Current execution result when fetched" : "Historical execution result"}</Text>
                    <Text selectable style={styles.mono}>SHA256 {artifactPreview.review.sha256}</Text>
                    {artifactPreview.review.members.map((member) => <View key={member.name} style={styles.item}>
                      <Text style={styles.jobID}>{member.name} · {member.size} bytes</Text>
                      {member.text === null ? <Empty>二进制或暂不支持预览的格式。</Empty> : <Text selectable style={styles.mono}>{member.text}</Text>}
                      {member.truncated && <Text style={styles.muted}>预览已截断。</Text>}
                    </View>)}
                    <Button title="关闭预览" onPress={() => setArtifactPreview(null)} />
                  </View>}
                </View>
              </View>

              <View style={styles.panel}>
                <View style={styles.rowBetween}>
                  <Text style={styles.sectionTitle}>执行事件</Text>
                  {!terminal.has(snapshot.job.state) && <StatePill value="LIVE" />}
                </View>
                <Events items={snapshot.events} />
              </View>
            </>
          )}
        </ScrollView>
      </View>
      <Text style={styles.footer}>任务操作受服务端权限与控制租约约束；重复提交通过幂等标识防止重复创建。</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, minHeight: "100%" as never, backgroundColor: "#0b0d10" },
  header: { flexWrap: "wrap", gap: 12, paddingHorizontal: 24, paddingTop: 24, paddingBottom: 16, flexDirection: "row", justifyContent: "space-between", alignItems: "flex-end", borderBottomWidth: 1, borderBottomColor: "#242830" },
  headerMeta: { flexDirection: "row", gap: 8 },
  eyebrow: { color: "#8b94a3", fontSize: 11, letterSpacing: 1.6, fontWeight: "700" },
  title: { color: "#f4f6f8", fontSize: 28, fontWeight: "700", marginTop: 4 },
  connection: { padding: 16, gap: 12, flexDirection: "row", alignItems: "flex-end", borderBottomWidth: 1, borderBottomColor: "#242830" },
  inputGroup: { flex: 1, minWidth: 180 },
  label: { color: "#aeb6c2", fontSize: 12, marginBottom: 6 },
  input: { color: "#f4f6f8", backgroundColor: "#15191f", borderWidth: 1, borderColor: "#303641", borderRadius: 8, paddingHorizontal: 12, paddingVertical: 10 },
  specInput: { minHeight: 110, marginTop: 8, textAlignVertical: "top" },
  button: { backgroundColor: "#e7ebf0", paddingHorizontal: 16, paddingVertical: 11, borderRadius: 8, alignItems: "center", justifyContent: "center" },
  buttonPressed: { opacity: 0.82 },
  buttonDisabled: { opacity: 0.4 },
  buttonText: { color: "#101318", fontWeight: "700" },
  actionRow: { flexDirection: "row", flexWrap: "wrap", alignItems: "center", gap: 8, marginTop: 10 },
  controlStack: { gap: 8, marginTop: 8 },
  errorBox: { marginHorizontal: 16, marginTop: 12, borderWidth: 1, borderColor: "#71343c", backgroundColor: "#2a161a", borderRadius: 8, padding: 10 },
  errorText: { color: "#ff9ea8" },
  body: { flex: 1, flexDirection: "row" },
  bodyCompact: { flexDirection: "column" },
  sidebar: { width: 330, borderRightWidth: 1, borderRightColor: "#242830", padding: 16 },
  sidebarCompact: { width: "100%", maxHeight: 400, borderRightWidth: 0, borderBottomWidth: 1, borderBottomColor: "#242830" },
  sectionTitle: { color: "#f4f6f8", fontSize: 15, fontWeight: "700", marginBottom: 10 },
  list: { maxHeight: 360 },
  workerList: { maxHeight: 260 },
  topGap: { marginTop: 22 },
  jobRow: { borderWidth: 1, borderColor: "#252b34", backgroundColor: "#11151a", borderRadius: 10, padding: 11, marginBottom: 8, gap: 5 },
  jobRowSelected: { borderColor: "#77869a", backgroundColor: "#181d24" },
  workerRow: { borderBottomWidth: 1, borderBottomColor: "#252b34", paddingVertical: 9, gap: 4 },
  rowBetween: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 8 },
  jobID: { color: "#e8ebef", fontWeight: "600", flexShrink: 1 },
  runtime: { color: "#bec6d1", fontSize: 12 },
  muted: { color: "#8993a2", fontSize: 12 },
  mono: { color: "#9fa9b8", fontSize: 11, fontFamily: "monospace" },
  capabilities: { color: "#93a2b5", fontSize: 11, marginTop: 5 },
  pill: { borderRadius: 999, borderWidth: 1, borderColor: "#3a424e", paddingHorizontal: 8, paddingVertical: 3, backgroundColor: "#171c22" },
  pillText: { color: "#c9d0d9", fontSize: 10, fontWeight: "700" },
  detail: { flex: 1 },
  detailContent: { padding: 18, gap: 14 },
  hero: { maxWidth: 720, paddingVertical: 64, paddingHorizontal: 16 },
  heroTitle: { color: "#f4f6f8", fontSize: 34, fontWeight: "700", marginBottom: 12 },
  heroText: { color: "#9da6b3", fontSize: 16, lineHeight: 24 },
  panel: { flex: 1, minWidth: 0, backgroundColor: "#11151a", borderWidth: 1, borderColor: "#252b34", borderRadius: 12, padding: 14 },
  detailTitle: { color: "#f4f6f8", fontSize: 20, fontWeight: "700", marginTop: 3, marginBottom: 5 },
  grid: { flexDirection: "row", flexWrap: "wrap", gap: 12 },
  gridCompact: { flexDirection: "column" },
  item: { borderTopWidth: 1, borderTopColor: "#242a32", paddingVertical: 9, gap: 4 },
  stack: { gap: 0 },
  event: { flexDirection: "row", gap: 10, paddingVertical: 7, borderTopWidth: 1, borderTopColor: "#20262e" },
  eventSeq: { color: "#77869a", fontFamily: "monospace", width: 64 },
  eventType: { color: "#dbe0e6", flex: 1 },
  eventTime: { color: "#7f8997", fontSize: 11 },
  footer: { color: "#707987", fontSize: 11, padding: 10, textAlign: "center", borderTopWidth: 1, borderTopColor: "#242830" },
});
