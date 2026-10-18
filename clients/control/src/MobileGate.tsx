import React, { useEffect, useRef, useState } from 'react';
import { AppState, Button, ScrollView, Text, TextInput, View } from 'react-native';
import { ControlAPI, ControlAPIError } from './api';
import { clearMobileProfile, initialConnectURL, loadMobileProfile, loadOrCreateDeviceID, saveMobileProfile, serverFromConnectURL, subscribeConnectURLs } from './mobile';
import { ConnectionEpoch, mobileServerURL, observeAppState } from './mobilePolicy';

export type MobileDraft = { spec: string; key: string };
export type MobileSession = { serverURL: string; token: string; deviceID: string };

/** Changing the connection unmounts the task UI and discards its cached authority. */
export function MobileGate({ children }: { children: (session: MobileSession, expired: () => void, disconnect: () => void, selected: React.MutableRefObject<string>, draft: React.MutableRefObject<MobileDraft>) => React.ReactNode }) {
  const [server, setServer] = useState('');
  const [token, setToken] = useState('');
  const [session, setSession] = useState<MobileSession | null>(null);
  const [active, setActive] = useState(AppState.currentState === 'active');
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const epoch = useRef(new ConnectionEpoch()).current;
  const currentSession = useRef<MobileSession | null>(null);
  const hydration = useRef(new ConnectionEpoch()).current;
  const selected = useRef('');
  const draft = useRef<MobileDraft>({spec:'{}', key:''});
  // Serialize Keychain writes so a pending save cannot undo a subsequent logout.
  const storage = useRef(Promise.resolve());
  const store = (operation: () => Promise<void>) => {
    const next = storage.current.then(operation);
    storage.current = next.catch(() => {});
    return next;
  };
  const reset = (message = '') => {
    epoch.invalidate(); hydration.invalidate(); currentSession.current = null; setSession(null); setToken(''); setBusy(false); selected.current = ''; draft.current = {spec:'{}',key:''}; setError(message);
    void store(clearMobileProfile).catch(() => setError('无法清除系统安全存储，请重试。'));
  };

  useEffect(() => {
    const ticket = hydration.begin();
    void loadMobileProfile().then(profile => {
      if (!hydration.current(ticket) || !profile) return;
      setServer(mobileServerURL(profile.serverURL)); setToken(profile.token);
    }).catch(() => { if (hydration.current(ticket)) setError('无法读取连接配置，请重新填写。'); })
      .finally(() => setReady(true));
    const apply = (value: string | null) => {
      if (!value) return;
      const target = serverFromConnectURL(value);
      if (target) {
        reset(); setServer(target); setReady(true);
      } else if (value.startsWith('computecloud:')) setError('连接链接无效；链接只能携带 HTTPS Server 地址，不能携带凭据。');
    };
    void initialConnectURL().then(apply).catch(() => {});
    const unsubscribe = subscribeConnectURLs(apply);
    const stop = observeAppState(AppState, foreground => {
      setActive(foreground);
      if (!foreground) { epoch.invalidate(); setBusy(false); }
    });
    return () => { epoch.invalidate(); hydration.invalidate(); unsubscribe(); stop(); };
  }, []);

  const connect = async () => {
    const ticket = epoch.begin(); setBusy(true); setError('');
    try {
      const serverURL = mobileServerURL(server);
      const key = token.trim();
      const client = new ControlAPI(serverURL, key);
      await client.bootstrap(ticket.signal);
      const deviceID = await loadOrCreateDeviceID();
      if (!epoch.current(ticket)) return;
      await store(async () => {
        if (epoch.current(ticket)) await saveMobileProfile(serverURL, key, deviceID);
      });
      if (epoch.current(ticket)) { const next = { serverURL, token: key, deviceID }; currentSession.current = next; setToken(''); setSession(next); }
    } catch (cause) {
      if (epoch.current(ticket) && cause instanceof ControlAPIError && cause.status === 401) { setToken(''); void store(clearMobileProfile).catch(() => {}); }
      if (epoch.current(ticket)) setError('连接失败：请检查 HTTPS 证书、Server 地址和 API key。');
    } finally { if (epoch.current(ticket)) setBusy(false); }
  };

  if (session) return active ? <View style={{flex:1}}>{children(session, () => { if (currentSession.current === session) reset('凭据已失效，请重新连接。'); }, () => reset(), selected, draft)}</View> : <View><Text>应用已进入后台，任务继续由 Server 执行。返回后重新同步。</Text></View>;
  return <ScrollView contentContainerStyle={{padding:24, gap:16, paddingTop:64}} keyboardShouldPersistTaps="handled">
    <Text accessibilityRole="header" style={{fontSize:26}}>连接 computecloud</Text>
    <Text>填写 Server 地址和原生任务 API key。验证成功后，凭据仅存入本设备系统安全存储。</Text>
    <Text>Server HTTPS 地址</Text>
    <TextInput accessibilityLabel="Server 地址" value={server} autoCapitalize="none" autoCorrect={false} onChangeText={value => { reset(); setServer(value); }} placeholder="https://control.example.com" style={{borderWidth:1,padding:12}} />
    <Text>API key</Text>
    <TextInput accessibilityLabel="API key" secureTextEntry autoCapitalize="none" autoCorrect={false} value={token} onChangeText={value => { epoch.invalidate(); setBusy(false); setToken(value); }} style={{borderWidth:1,padding:12}} />
    {!!error && <Text accessibilityRole="alert">{error}</Text>}
    <Button title={busy ? '正在验证连接…' : '连接'} disabled={!ready || !active || busy || !server.trim() || !token.trim()} onPress={() => void connect()} />
    <Button title="清除保存的连接" onPress={() => reset()} disabled={!ready} />
    <Text>公网连接需要可信 HTTPS 证书；自签证书须先由设备信任。切换 Server 会清除旧凭据，离线不排队提交操作。</Text>
  </ScrollView>;
}
