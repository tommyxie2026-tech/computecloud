import type {
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

  bootstrap(): Promise<ControlBootstrap> {
    return this.json("/v1/control/bootstrap");
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
    return {
      job,
      tasks: tasks.tasks,
      sessions: sessions.sessions,
      approvals: approvals.approvals,
      artifacts: artifacts.artifacts,
      events: events.events,
    };
  }

  artifactURL(jobID: string, artifactID: string): string {
    return `${this.baseURL}/v1/jobs/${encodeURIComponent(jobID)}/artifacts/${encodeURIComponent(artifactID)}`;
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
