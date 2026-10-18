import React, { useEffect, useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { getSession, login, logout, type WebSession } from "./webAuth";
export function WebGate({ children }: { children: (session: WebSession, expired: () => void, signOut: () => void) => React.ReactNode }) {
  const [session, setSession] = useState<WebSession | null>(null);
  const [checking, setChecking] = useState(true);
  const [busy, setBusy] = useState(false);
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  useEffect(() => { let active = true; void getSession().then(s => { if (active) setSession(s); }).catch(() => {}).finally(() => { if (active) setChecking(false); }); return () => { active = false; }; }, []);
  useEffect(() => { if (!session) return; const timer = setTimeout(() => { setSession(null); setError("登录已过期，请重新登录"); }, Math.max(0, session.expires_at_ms - Date.now())); return () => clearTimeout(timer); }, [session]);
  const expired = () => { setSession(null); setPassword(""); setError("登录已失效，请重新登录"); };
  const signOut = async () => { if (!session || busy) return; setBusy(true); try { await logout(session.csrf_token); setSession(null); setPassword(""); setError(""); } catch (e) { setError(e instanceof Error ? e.message : "退出失败"); } finally { setBusy(false); } };
  const submit = async () => { if (busy) return; setBusy(true); setError(""); try { const value = await login(username, password); setPassword(""); setSession(value); } catch (e) { setPassword(""); setError(e instanceof Error ? e.message : "登录失败"); } finally { setBusy(false); } };
  if (checking) return <View style={styles.screen}><ActivityIndicator accessibilityLabel="正在检查登录状态" /></View>;
  if (session) return <View style={{ flex: 1 }}>{!!error && <Text accessibilityRole="alert" style={styles.error}>{error}</Text>}{children(session, expired, () => { void signOut(); })}</View>;
  return <View style={styles.screen}><View style={styles.card}>
    <Text style={styles.brand}>COMPUTECLOUD</Text><Text style={styles.title}>任务控制台</Text><Text style={styles.subtitle}>登录以提交任务、跟踪执行并查看结果</Text>
    <Text style={styles.label}>用户名</Text><TextInput accessibilityLabel="用户名" autoCapitalize="none" autoComplete="username" value={username} onChangeText={setUsername} style={styles.input} />
    <Text style={styles.label}>密码</Text><TextInput accessibilityLabel="密码" secureTextEntry autoComplete="current-password" value={password} onChangeText={setPassword} onSubmitEditing={() => { void submit(); }} style={styles.input} />
    {!!error && <Text accessibilityRole="alert" style={styles.error}>{error}</Text>}
    <Pressable accessibilityRole="button" accessibilityLabel="登录" disabled={busy || !username || !password} onPress={() => { void submit(); }} style={[styles.button, busy && { opacity: 0.5 }]}><Text style={styles.buttonText}>{busy ? "正在登录…" : "登录"}</Text></Pressable>
  </View></View>;
}
const styles = StyleSheet.create({ screen: { flex: 1, minHeight: "100%" as never, justifyContent: "center", alignItems: "center", padding: 24, backgroundColor: "#0b0d10" }, card: { width: "100%", maxWidth: 420, padding: 28, borderWidth: 1, borderColor: "#303641", borderRadius: 16, backgroundColor: "#11151a", gap: 10 }, brand: { color: "#93a2b5", fontSize: 12, letterSpacing: 2 }, title: { color: "#f4f6f8", fontSize: 28, fontWeight: "700" }, subtitle: { color: "#9da6b3", marginBottom: 12 }, label: { color: "#c9d0d9", marginTop: 8 }, input: { color: "#f4f6f8", borderWidth: 1, borderColor: "#303641", borderRadius: 8, padding: 12 }, error: { color: "#ff9ea8", padding: 8 }, button: { backgroundColor: "#e7ebf0", padding: 13, borderRadius: 8, marginTop: 12, alignItems: "center" }, buttonText: { fontWeight: "700", color: "#101318" } });
