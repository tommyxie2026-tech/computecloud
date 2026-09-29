export type JobSummary = {
  job_id: string;
  project: string;
  state: string;
  mode: string;
  version: string;
  last_seq: string;
  created_at_ms: string;
  updated_at_ms: string;
  deadline_ms: string;
  stop_reason?: string;
  error_code?: string;
};

export type JobCursor = {
  created_at_ms: string;
  job_id: string;
};

export type JobPage = {
  server_epoch: string;
  snapshot_ms: string;
  snapshot_id: string;
  jobs: JobSummary[];
  next?: JobCursor;
  has_more: boolean;
};

export type WorkerRuntime = {
  profile: string;
  version?: string;
  capabilities: string[];
  control_capabilities: string[];
};

export type WorkerView = {
  worker_id: string;
  online: boolean;
  slots: number;
  active: number;
  last_seen_ms: string;
  runtimes: WorkerRuntime[];
};

export type WorkerPage = {
  server_epoch: string;
  observed_at_ms: string;
  workers: WorkerView[];
  next?: string;
  has_more: boolean;
};

export type ControlBootstrap = {
  protocol_min: string;
  protocol_max: string;
  server_epoch: string;
  read_only: boolean;
  runtimes: Array<{
    profile: string;
    version?: string;
    capabilities: string[];
  }>;
};

export type JobDetail = {
  job_id: string;
  state: string;
  mode: string;
  existing: boolean;
  last_seq: string;
  version: string;
  created_at_ms: string;
  updated_at_ms: string;
  deadline_ms: string;
  stop_reason?: string;
  error_code?: string;
  counts?: Record<string, Record<string, number>>;
  scheduling_blockers?: Record<string, number>;
  poll_after_ms?: number;
  usage?: Record<string, unknown>;
  links?: Record<string, string>;
};

export type TaskView = {
  task_id: string;
  state: string;
  stage: string;
  partition_key: string;
  attempt_id: string;
  worker_id: string;
  error_code: string;
  scheduling_blocker: string;
  last_renewed_ms: string;
  lease_until_ms: string;
};

export type SessionView = {
  protocol_version: string;
  session_id: string;
  job_id: string;
  task_id: string;
  attempt_id: string;
  generation: number;
  worker_id?: string;
  runtime: string;
  runtime_version?: string;
  runtime_session_ref?: string;
  state: string;
  capabilities: string[];
};

export type ApprovalView = {
  approval_id: string;
  task_id: string;
  attempt_id: string;
  generation: number;
  session_id: string;
  tool: string;
  action: string;
  risk_class: string;
  arguments_summary?: string;
  request_version: number;
  state: string;
};

export type ArtifactView = {
  artifact_id: string;
  task_id: string;
  attempt_id: string;
  kind: string;
  sha256: string;
  size: number;
  download: string;
};

export type JobEvent = {
  seq: string;
  type: string;
  recorded_at_ms: string;
  payload: unknown;
};

export type JobSnapshot = {
  job: JobDetail;
  tasks: TaskView[];
  sessions: SessionView[];
  approvals: ApprovalView[];
  artifacts: ArtifactView[];
  events: JobEvent[];
};
