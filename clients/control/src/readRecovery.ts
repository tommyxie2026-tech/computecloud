import type { ControlAPI } from "./api";
import type { JobSnapshot } from "./types";
function abortError(): Error { const error = new Error("Observation aborted"); error.name = "AbortError"; return error; }
export async function readSnapshotWithRetry(client: Pick<ControlAPI, "jobSnapshot">, jobID: string, signal: AbortSignal, onRetry: () => void): Promise<JobSnapshot> {
  let delay = 1000;
  while (!signal.aborted) {
    try {
      const snapshot = await client.jobSnapshot(jobID, signal);
      if (signal.aborted) throw abortError();
      return snapshot;
    } catch (error) {
      if (signal.aborted) throw abortError();
      const status = error && typeof error === "object" && "status" in error ? Number(error.status) : 0;
      if (!(error instanceof TypeError) && status !== 429 && status < 500) throw error;
      onRetry();
      await new Promise<void>((resolve, reject) => {
        if (signal.aborted) { reject(abortError()); return; }
        const stop = () => { clearTimeout(timer); reject(abortError()); };
        const timer = setTimeout(() => { signal.removeEventListener("abort", stop); resolve(); }, delay);
        signal.addEventListener("abort", stop, { once: true });
      });
      delay = Math.min(delay * 2, 10000);
    }
  }
  throw abortError();
}
export function subscribeConnectivity(target: EventTarget, offline: () => void, online: () => void): () => void {
  target.addEventListener("offline", offline);
  target.addEventListener("online", online);
  return () => { target.removeEventListener("offline", offline); target.removeEventListener("online", online); };
}
