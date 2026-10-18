export type WebSession = { username: string; expires_at_ms: number; csrf_token: string };
export class WebAuthError extends Error { constructor(public readonly status: number, message: string) { super(message); this.name = "WebAuthError"; } }
async function request(path: string, init: RequestInit = {}): Promise<WebSession | undefined> {
  const response = await fetch(`/v1/web/auth/${path}`, { ...init, credentials: "same-origin", cache: "no-store" });
  if (!response.ok) {
    const message = response.status === 401 ? "用户名或密码错误，或登录已失效" : response.status === 429 ? "登录尝试过于频繁，请稍后再试" : "连接失败，请检查网络和服务配置";
    throw new WebAuthError(response.status, message);
  }
  return response.status === 204 ? undefined : await response.json() as WebSession;
}
export async function login(username: string, password: string): Promise<WebSession> {
  return await request("login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ username, password }) }) as WebSession;
}
export async function getSession(): Promise<WebSession> { return await request("session") as WebSession; }
export async function logout(csrfToken: string): Promise<void> { await request("logout", { method: "POST", headers: { "X-CSRF-Token": csrfToken } }); }
