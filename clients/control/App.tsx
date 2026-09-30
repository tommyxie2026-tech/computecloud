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

import { ControlAPI, isSnapshotReset } from "./src/api";
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

function StatePill({ value }: { value: string }) {
  return <View style={styles.pill}><Text style={styles.pillText}>{value}</Text></View>;
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
      {!items.length && <Empty>No events yet.</Empty>}
    </View>
  );
}

export default function App() {
  const { width } = useWindowDimensions();
  const compact = width < 960;
  const [serverURL, setServerURL] = useState(process.env.EXPO_PUBLIC_COMPUTECLOUD_URL ?? "http://127.0.0.1:8080");
  const [token, setToken] = useState("");
  const [api, setAPI] = useState<ControlAPI | null>(null);
  const [bootstrap, setBootstrap] = useState<ControlBootstrap | null>(null);
  const [jobPage, setJobPage] = useState<JobPage | null>(null);
  const [workers, setWorkers] = useState<WorkerPage | null>(null);
  const [selected, setSelected] = useState<string>("");
  const [snapshot, setSnapshot] = useState<JobSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [writeLease, setWriteLease] = useState<ControlWriteLease | null>(null);
  const [sessionInput, setSessionInput] = useState("");
  const [newJobSpec, setNewJobSpec] = useState("{}");
  const [newJobKey, setNewJobKey] = useState(operationID("job"));
  const streamAbort = useRef<AbortController | null>(null);
  const holderID = useRef(operationID("device"));

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
    setJobPage(jobs);
    setWorkers(workerPage);
  }, []);

  const connect = useCallback(async () => {
    streamAbort.current?.abort();
    setBusy(true);
    setError("");
    try {
      const client = new ControlAPI(serverURL, token);
      const boot = await client.bootstrap();
      await refreshCollections(client, boot.server_epoch);
      setAPI(client);
      setBootstrap(boot);
      setSelected("");
      setSnapshot(null);
      setWriteLease(null);
    } catch (e) {
      setAPI(null);
      setBootstrap(null);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [refreshCollections, serverURL, token]);

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
    if (!api) return;
    streamAbort.current?.abort();
    if (writeLease && selected) {
      try { await api.releaseWriteLease(selected, holderID.current, writeLease.lease_token); } catch { /* lease expires quickly */ }
      setWriteLease(null);
    }
    const controller = new AbortController();
    streamAbort.current = controller;
    setSelected(jobID);
    setSnapshot(null);
    setBusy(true);
    setError("");
    try {
      const initial = await api.jobSnapshot(jobID);
      setSnapshot(initial);
      setBusy(false);
      let cursor = initial.events.at(-1)?.seq ?? "0";
      if (terminal.has(initial.job.state)) return;
      await api.streamEvents(jobID, cursor, controller.signal, (event) => {
        cursor = event.seq;
        setSnapshot((current) => current ? {
          ...current,
          events: current.events.some((item) => item.seq === event.seq) ? current.events : [...current.events, event],
        } : current);
      });
      if (!controller.signal.aborted) {
        const latest = await api.jobSnapshot(jobID);
        setSnapshot(latest);
      }
    } catch (e) {
      if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }, [api, selected, writeLease]);

  useEffect(() => () => streamAbort.current?.abort(), []);

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

  const submitNewJob = useCallback(async () => {
    if (!api) return;
    setBusy(true);
    setError("");
    try {
      const spec = JSON.parse(newJobSpec) as unknown;
      const created = await api.submitJob(spec, newJobKey);
      setNewJobKey(operationID("job"));
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

  const openArtifact = useCallback((artifactID: string) => {
    if (!api || !selected) return;
    // Browser downloads still require Authorization; opening the raw URL cannot
    // safely carry a bearer token. C1 therefore surfaces metadata only and
    // leaves authenticated download to a later explicit client action.
    setError(`Authenticated artifact download is not enabled in C1 (${artifactID}).`);
  }, [api, selected]);

  return (
    <View style={styles.root}>
      <View style={styles.header}>
        <View>
          <Text style={styles.eyebrow}>AGENT JOB EXECUTOR</Text>
          <Text style={styles.title}>computecloud Control</Text>
        </View>
        <View style={styles.headerMeta}>
          {bootstrap ? <StatePill value={`ACP ${bootstrap.protocol_max}`} /> : <StatePill value="Disconnected" />}
          {!!attention && <StatePill value={`${attention} attention`} />}
        </View>
      </View>

      <View style={styles.connection}>
        <View style={styles.inputGroup}>
          <Text style={styles.label}>Server</Text>
          <TextInput autoCapitalize="none" value={serverURL} onChangeText={setServerURL} style={styles.input} placeholder="https://control.example.com" />
        </View>
        <View style={styles.inputGroup}>
          <Text style={styles.label}>Bearer token · memory only</Text>
          <TextInput autoCapitalize="none" secureTextEntry value={token} onChangeText={setToken} style={styles.input} placeholder="Paste token" />
        </View>
        <Button title={busy && !api ? "Connecting…" : "Connect"} disabled={busy || !token.trim()} onPress={connect} />
      </View>

      {!!error && <View style={styles.errorBox}><Text style={styles.errorText}>{error}</Text></View>}

      <View style={[styles.body, compact && styles.bodyCompact]}>
        <View style={[styles.sidebar, compact && styles.sidebarCompact]}>
          <Text style={styles.sectionTitle}>New Job</Text>
          <Text style={styles.muted}>Submit the canonical Job JSON. The Server validates project, Runtime, credential, template and policy.</Text>
          <TextInput
            multiline
            value={newJobSpec}
            onChangeText={setNewJobSpec}
            style={[styles.input, styles.specInput]}
            placeholder='{"schema_version":"v0.2", ...}'
            placeholderTextColor="#66717f"
          />
          <Text style={styles.mono}>Idempotency-Key {newJobKey}</Text>
          <View style={styles.actionRow}>
            <Button title="Submit Job" onPress={submitNewJob} disabled={busy || !api} />
            <Button title="New key" onPress={() => setNewJobKey(operationID("job"))} disabled={busy} />
          </View>

          <Text style={[styles.sectionTitle, styles.topGap]}>Jobs</Text>
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
            {!jobPage?.jobs.length && <Empty>Connect to load jobs.</Empty>}
          </ScrollView>
          {jobPage?.has_more && <Button title="Load more" onPress={loadMoreJobs} disabled={busy} />}

          <Text style={[styles.sectionTitle, styles.topGap]}>Workers</Text>
          <ScrollView style={styles.workerList}>
            {workers?.workers.map((worker) => (
              <View key={worker.worker_id} style={styles.workerRow}>
                <View style={styles.rowBetween}>
                  <Text style={styles.jobID}>{worker.worker_id}</Text>
                  <StatePill value={worker.online ? "ONLINE" : "OFFLINE"} />
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
          {!selected && <View style={styles.hero}><Text style={styles.heroTitle}>Operate durable Agent execution</Text><Text style={styles.heroText}>Submit a Job or select an existing Job to inspect and control its durable Tasks, Attempts, Sessions, Approvals, Artifacts and event stream.</Text></View>}
          {busy && selected && !snapshot && <ActivityIndicator />}
          {snapshot && (
            <>
              <View style={styles.panel}>
                <View style={styles.rowBetween}>
                  <View>
                    <Text style={styles.eyebrow}>JOB</Text>
                    <Text style={styles.detailTitle}>{snapshot.job.job_id}</Text>
                  </View>
                  <StatePill value={snapshot.job.state} />
                </View>
                <Text style={styles.muted}>mode {snapshot.job.mode} · seq {snapshot.job.last_seq} · updated {formatTime(snapshot.job.updated_at_ms)}</Text>
                {!!snapshot.job.error_code && <Text style={styles.errorText}>{snapshot.job.error_code}</Text>}
                <View style={styles.actionRow}>
                  {writeLease
                    ? <Button title="Release control" onPress={releaseControl} disabled={busy} />
                    : <Button title="Take control" onPress={takeControl} disabled={busy || bootstrap?.read_only || terminal.has(snapshot.job.state)} />}
                  {writeLease && !terminal.has(snapshot.job.state) && <Button title="Cancel job" onPress={cancelSelected} disabled={busy} />}
                  {writeLease && <StatePill value="WRITE LEASE" />}
                </View>
              </View>

              <View style={[styles.grid, compact && styles.gridCompact]}>
                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>Tasks / Attempts</Text>
                  {snapshot.tasks.map((task) => (
                    <View key={task.task_id} style={styles.item}>
                      <View style={styles.rowBetween}><Text style={styles.jobID}>{task.stage || "single"} · {task.partition_key}</Text><StatePill value={task.state} /></View>
                      <Text style={styles.mono}>task {task.task_id}</Text>
                      <Text style={styles.mono}>attempt {task.attempt_id || "—"} · gen {task.generation || "0"}</Text>
                      <Text style={styles.muted}>{task.worker_id || "unassigned"} {task.scheduling_blocker ? `· ${task.scheduling_blocker}` : ""}</Text>
                      {!!task.error_code && <Text style={styles.errorText}>{task.error_code}</Text>}
                      {writeLease && snapshot.job.mode === "single" && snapshot.job.state === "FAILED" && task.state === "FAILED" && !!task.attempt_id && (
                        <View style={styles.controlStack}>
                          <Button title="Retry failed task" onPress={() => retryTask(task)} disabled={busy} />
                          <Text style={styles.muted}>Server revalidates replay safety, cleanup proof, deadline, attempt budget and generation.</Text>
                        </View>
                      )}
                    </View>
                  ))}
                </View>

                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>Sessions</Text>
                  {snapshot.sessions.map((session) => (
                    <View key={session.session_id} style={styles.item}>
                      <View style={styles.rowBetween}><Text style={styles.jobID}>{session.runtime}</Text><StatePill value={session.state} /></View>
                      <Text style={styles.muted}>{session.runtime_version ?? "version unknown"} · gen {session.generation}</Text>
                      <Text style={styles.capabilities}>{session.capabilities.join(" · ") || "no interactive capabilities"}</Text>
                      {writeLease && session.capabilities.includes("interactive_input") && (
                        <View style={styles.controlStack}>
                          <TextInput value={sessionInput} onChangeText={setSessionInput} style={styles.input} placeholder="Send input to this session" placeholderTextColor="#66717f" />
                          <Button title="Send input" onPress={() => sendSessionInput(session)} disabled={busy || !sessionInput.trim()} />
                        </View>
                      )}
                      {writeLease && session.capabilities.includes("session_resume") && (
                        <View style={styles.controlStack}><Button title="Resume session" onPress={() => resumeSession(session)} disabled={busy} /></View>
                      )}
                    </View>
                  ))}
                  {!snapshot.sessions.length && <Empty>No sessions.</Empty>}
                </View>

                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>Attention / Approvals</Text>
                  {snapshot.approvals.map((approval) => (
                    <View key={approval.approval_id} style={styles.item}>
                      <View style={styles.rowBetween}><Text style={styles.jobID}>{approval.tool}</Text><StatePill value={approval.state} /></View>
                      <Text>{approval.action}</Text>
                      <Text style={styles.muted}>{approval.risk_class} · request v{approval.request_version}</Text>
                      {!!approval.arguments_summary && <Text style={styles.mono}>{approval.arguments_summary}</Text>}
                      {writeLease && approval.state === "PENDING" && (
                        <View style={styles.actionRow}>
                          <Button title="Approve" onPress={() => decideApproval(approval, "ACCEPT")} disabled={busy} />
                          <Button title="Reject" onPress={() => decideApproval(approval, "REJECT")} disabled={busy} />
                        </View>
                      )}
                    </View>
                  ))}
                  {!snapshot.approvals.length && <Empty>No pending or historical approvals.</Empty>}
                </View>

                <View style={styles.panel}>
                  <Text style={styles.sectionTitle}>Artifacts</Text>
                  {snapshot.artifacts.map((artifact) => (
                    <Pressable key={artifact.artifact_id} onPress={() => openArtifact(artifact.artifact_id)} style={styles.item}>
                      <Text style={styles.jobID}>{artifact.kind}</Text>
                      <Text style={styles.mono}>{artifact.artifact_id}</Text>
                      <Text style={styles.muted}>{artifact.size} bytes · sha256 {artifact.sha256.slice(0, 12)}…</Text>
                    </Pressable>
                  ))}
                  {!snapshot.artifacts.length && <Empty>No accepted artifacts.</Empty>}
                </View>
              </View>

              <View style={styles.panel}>
                <View style={styles.rowBetween}>
                  <Text style={styles.sectionTitle}>Durable event stream</Text>
                  {!terminal.has(snapshot.job.state) && <StatePill value="LIVE" />}
                </View>
                <Events items={snapshot.events} />
              </View>
            </>
          )}
        </ScrollView>
      </View>
      <Text style={styles.footer}>C2 Operate · Existing Job writes require a short-lived single-writer lease · Job submit is idempotency-keyed · Bearer and lease tokens stay memory-only.</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, minHeight: "100%" as never, backgroundColor: "#0b0d10" },
  header: { paddingHorizontal: 24, paddingTop: 24, paddingBottom: 16, flexDirection: "row", justifyContent: "space-between", alignItems: "flex-end", borderBottomWidth: 1, borderBottomColor: "#242830" },
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
