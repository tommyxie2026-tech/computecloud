/** Native credentials are scoped to a single, explicit Server origin. */
export function mobileServerURL(value: string): string {
  const url = new URL(value.trim());
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if ((url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) ||
      url.username || url.password || url.search || url.hash || url.pathname !== '/') {
    throw new Error('请输入 HTTPS Server 地址（本机回环地址允许 HTTP），不要包含路径、凭据或查询参数。');
  }
  return url.origin;
}

export class ConnectionEpoch {
  private controller?: AbortController;
  begin(): AbortController {
    this.invalidate();
    return this.controller = new AbortController();
  }
  current(value: AbortController): boolean { return this.controller === value && !value.signal.aborted; }
  invalidate(): void { this.controller?.abort(); this.controller = undefined; }
}

type AppStateSource<State extends string> = {
  currentState: State | null;
  addEventListener(event: 'change', listener: (state: State) => void): { remove(): void };
};
export function observeAppState<State extends string>(source: AppStateSource<State>, change: (active: boolean) => void): () => void {
  let previous: boolean | undefined;
  const update = (state: string | null) => {
    const active = state === 'active';
    if (active !== previous) { previous = active; change(active); }
  };
  update(source.currentState);
  const subscription = source.addEventListener('change', update);
  return () => subscription.remove();
}
