import type {
  GoalView,
  ArtifactReview,
  ArtifactView,
  ApprovalView,
  ControlBootstrap,
  JobDetail,
  JobEvent,
  JobPage,
  JobSnapshot,
  SessionView,
  TaskView,
  WorkerPage,
  ControlWriteLease,
  ControlOperationReceipt,
  ManualRetryReceipt,
} from "./types";

export class ControlAPIError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
  ) {
    super(code);
    this.name = "ControlAPIError";
  }
}

function trimBaseURL(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

export class ControlAPI {
  readonly baseURL: string;

  constructor(baseURL: string, private readonly token: string) {
    this.baseURL = trimBaseURL(baseURL);
    if (!this.baseURL) throw new Error("Server URL is required");
    if (!token.trim()) throw new Error("Bearer token is required");
  }

  private async fetch(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers);
    headers.set("Authorization", `Bearer ${this.token}`);
    headers.set("Accept", "application/json, text/event-stream");
    const response = await globalThis.fetch(this.baseURL + path, { ...init, headers });
    if (!response.ok) {
      let code = `HTTP_${response.status}`;
      try {
        const body = await response.json() as { error?: { code?: string } };
        code = body.error?.code || code;
      } catch {
        // Keep a transport-level error code when a proxy returns non-JSON.
      }
      throw new ControlAPIError(response.status, code);
    }
    return response;
  }

  private async json<T>(path: string): Promise<T> {
    return await (await this.fetch(path)).json() as T;
  }

  private async writeJSON<T>(path: string, method: string, body: unknown, leaseToken?: string): Promise<T> {
    const headers = new Headers({ "Content-Type": "application/json" });
    if (leaseToken) headers.set("X-Control-Lease", leaseToken);
    const response = await this.fetch(path, {
      method,
      headers,
      body: JSON.stringify(body),
    });
    if (response.status === 204) return undefined as T;
    return await response.json() as T;
  }

  bootstrap(): Promise<ControlBootstrap> {
    return this.json("/v1/control/bootstrap");
  }

  submitJob(spec: unknown, idempotencyKey: string): Promise<JobDetail> {
    const headers = new Headers({
      "Content-Type": "application/json",
      "Idempotency-Key": idempotencyKey,
    });
    return this.fetch("/v1/jobs", {
      method: "POST",
      headers,
      body: JSON.stringify(spec),
    }).then(async (response) => await response.json() as JobDetail);
  }

  manualRetry(jobID: string, task: TaskView, leaseToken: string, operationID: string): Promise<ManualRetryReceipt> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/retry`, "POST", {
      operation_id: operationID,
      task_id: task.task_id,
      expected_attempt_id: task.attempt_id,
      expected_generation: Number(task.generation),
    }, leaseToken);
  }


  listJobs(query: URLSearchParams = new URLSearchParams()): Promise<JobPage> {
    const suffix = query.size ? `?${query.toString()}` : "";
    return this.json("/v1/jobs" + suffix);
  }

  listWorkers(query: URLSearchParams = new URLSearchParams()): Promise<WorkerPage> {
    const suffix = query.size ? `?${query.toString()}` : "";
    return this.json("/v1/workers" + suffix);
  }

  async jobSnapshot(jobID: string): Promise<JobSnapshot> {
    const id = encodeURIComponent(jobID);
    const [job, tasks, sessions, approvals, artifacts, events] = await Promise.all([
      this.json<JobDetail>(`/v1/jobs/${id}`),
      this.json<{ tasks: TaskView[] }>(`/v1/jobs/${id}/tasks?limit=100`),
      this.json<{ sessions: SessionView[] }>(`/v1/jobs/${id}/sessions`),
      this.json<{ approvals: ApprovalView[] }>(`/v1/jobs/${id}/approvals`),
      this.json<{ artifacts: ArtifactView[] }>(`/v1/jobs/${id}/artifacts?limit=100`),
      this.json<{ events: JobEvent[] }>(`/v1/jobs/${id}/events?after_seq=0&limit=500`),
    ]);
    const goal = job.links?.goal ? await this.json<GoalView>(`/v1/jobs/${id}/goal`) : undefined;
    return {
      goal,
      job,
      tasks: tasks.tasks,
      sessions: sessions.sessions,
      approvals: approvals.approvals,
      artifacts: artifacts.artifacts,
      events: events.events,
    };
  }

  artifactReview(jobID: string, artifactID: string): Promise<ArtifactReview> {
    return this.json(`/v1/jobs/${encodeURIComponent(jobID)}/artifacts/${encodeURIComponent(artifactID)}/review`);
  }

  artifactURL(jobID: string, artifactID: string): string {
    return `${this.baseURL}/v1/jobs/${encodeURIComponent(jobID)}/artifacts/${encodeURIComponent(artifactID)}`;
  }

  acquireWriteLease(jobID: string, holderID: string): Promise<ControlWriteLease> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/control-lease`, "POST", { holder_id: holderID });
  }

  renewWriteLease(jobID: string, holderID: string, leaseToken: string): Promise<ControlWriteLease> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/control-lease`, "PUT", { holder_id: holderID }, leaseToken);
  }

  releaseWriteLease(jobID: string, holderID: string, leaseToken: string): Promise<void> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/control-lease`, "DELETE", { holder_id: holderID }, leaseToken);
  }

  cancelJob(jobID: string, leaseToken: string, controlID: string, reason: string): Promise<JobDetail> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/control/cancel`, "POST",
      { control_id: controlID, reason }, leaseToken);
  }

  sendInput(jobID: string, session: SessionView, leaseToken: string, operationID: string, resourceVersion: number, content: string, mode = "interactive"): Promise<ControlOperationReceipt> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/sessions/${encodeURIComponent(session.session_id)}/inputs`, "POST", {
      operation_id: operationID,
      task_id: session.task_id,
      expected_attempt_id: session.attempt_id,
      expected_generation: session.generation,
      expected_resource_version: resourceVersion,
      mode,
      content,
    }, leaseToken);
  }

  resumeSession(jobID: string, session: SessionView, leaseToken: string, operationID: string, resourceVersion: number): Promise<ControlOperationReceipt> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/sessions/${encodeURIComponent(session.session_id)}/resume`, "POST", {
      operation_id: operationID,
      task_id: session.task_id,
      expected_attempt_id: session.attempt_id,
      expected_generation: session.generation,
      expected_resource_version: resourceVersion,
    }, leaseToken);
  }

  decideApproval(jobID: string, approval: ApprovalView, leaseToken: string, operationID: string, resourceVersion: number, decision: "ACCEPT" | "REJECT"): Promise<ControlOperationReceipt> {
    return this.writeJSON(`/v1/jobs/${encodeURIComponent(jobID)}/approvals/${encodeURIComponent(approval.approval_id)}`, "POST", {
      operation_id: operationID,
      task_id: approval.task_id,
      expected_attempt_id: approval.attempt_id,
      expected_generation: approval.generation,
      expected_resource_version: resourceVersion,
      request_version: approval.request_version,
      decision,
    }, leaseToken);
  }

  async streamEvents(
    jobID: string,
    afterSeq: string,
    signal: AbortSignal,
    onEvent: (event: JobEvent) => void,
  ): Promise<void> {
    const id = encodeURIComponent(jobID);
    const response = await this.fetch(`/v1/jobs/${id}/events/stream?after_seq=${encodeURIComponent(afterSeq)}`, { signal });
    if (!response.body) throw new Error("Streaming response body unavailable");

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let pending = "";

    const consume = (block: string) => {
      let eventID = "";
      let eventType = "message";
      const data: string[] = [];
      for (const line of block.split(/\r?\n/)) {
        if (line.startsWith("id:")) eventID = line.slice(3).trim();
        else if (line.startsWith("event:")) eventType = line.slice(6).trim();
        else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (!data.length) return;
      const parsed = JSON.parse(data.join("\n")) as JobEvent;
      if (eventID) parsed.seq = eventID;
      if (!parsed.type) parsed.type = eventType;
      onEvent(parsed);
    };

    try {
      while (!signal.aborted) {
        const { value, done } = await reader.read();
        pending += decoder.decode(value, { stream: !done });
        let boundary: number;
        while ((boundary = pending.indexOf("\n\n")) >= 0) {
          const block = pending.slice(0, boundary);
          pending = pending.slice(boundary + 2);
          consume(block);
        }
        if (done) {
          if (pending.trim()) consume(pending);
          return;
        }
      }
    } finally {
      reader.releaseLock();
    }
  }
}

export function isSnapshotReset(error: unknown): boolean {
  return error instanceof ControlAPIError && error.code === "SNAPSHOT_EPOCH_CHANGED";
}
