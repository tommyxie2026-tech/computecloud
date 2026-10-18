import type { JobSnapshot } from "./types";
export function mergeJobSnapshot(current: JobSnapshot | null, latest: JobSnapshot): JobSnapshot {
  if (!current || current.job.job_id !== latest.job.job_id || BigInt(latest.job.last_seq) < BigInt(current.job.last_seq)) return latest;
  const events = new Map([...latest.events, ...current.events].map(event => [event.seq, event]));
  return { ...latest, events: [...events.values()].sort((a,b) => BigInt(a.seq) < BigInt(b.seq) ? -1 : BigInt(a.seq) > BigInt(b.seq) ? 1 : 0).slice(-500) };
}
export function jobFromHash(hash: string): string | null {
  const value = new URLSearchParams(hash.replace(/^#/, "")).get("job");
  return value && /^[A-Za-z0-9_-]{1,128}$/.test(value) ? value : null;
}
export function jobHash(jobID: string): string { return "#" + new URLSearchParams({ job: jobID }).toString(); }
