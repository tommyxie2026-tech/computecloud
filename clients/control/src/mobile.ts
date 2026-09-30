import { Linking, Platform } from "react-native";
import * as SecureStore from "expo-secure-store";

const profileKey = "computecloud.control.profile.v1";
const deviceKey = "computecloud.control.device.v1";

export type MobileConnectionProfile = {
  serverURL: string;
  token: string;
  deviceID: string;
};

function newDeviceID(): string {
  const random = Math.random().toString(36).slice(2);
  return `device-${Date.now().toString(36)}-${random}`;
}

export function isNativeMobile(): boolean {
  return Platform.OS === "ios" || Platform.OS === "android";
}

export async function loadMobileProfile(): Promise<MobileConnectionProfile | null> {
  if (!isNativeMobile()) return null;
  const raw = await SecureStore.getItemAsync(profileKey);
  if (!raw) return null;
  try {
    const value = JSON.parse(raw) as Partial<MobileConnectionProfile>;
    if (!value.serverURL || !value.token || !value.deviceID) return null;
    return {
      serverURL: value.serverURL,
      token: value.token,
      deviceID: value.deviceID,
    };
  } catch {
    await SecureStore.deleteItemAsync(profileKey);
    return null;
  }
}

export async function saveMobileProfile(serverURL: string, token: string, deviceID: string): Promise<void> {
  if (!isNativeMobile()) return;
  await SecureStore.setItemAsync(profileKey, JSON.stringify({ serverURL, token, deviceID }), {
    keychainAccessible: SecureStore.AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY,
  });
}

export async function clearMobileProfile(): Promise<void> {
  if (!isNativeMobile()) return;
  await SecureStore.deleteItemAsync(profileKey);
}

export async function loadOrCreateDeviceID(): Promise<string> {
  if (!isNativeMobile()) return newDeviceID();
  const existing = await SecureStore.getItemAsync(deviceKey);
  if (existing) return existing;
  const created = newDeviceID();
  await SecureStore.setItemAsync(deviceKey, created, {
    keychainAccessible: SecureStore.AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY,
  });
  return created;
}

export function serverFromConnectURL(value: string): string | null {
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "computecloud:" || parsed.hostname !== "connect") return null;
    if (parsed.searchParams.has("token") || parsed.searchParams.has("authorization")) return null;
    const server = parsed.searchParams.get("server")?.trim();
    if (!server) return null;
    const target = new URL(server);
    if (target.protocol !== "https:" && target.protocol !== "http:") return null;
    return target.toString().replace(/\/$/, "");
  } catch {
    return null;
  }
}

export async function initialConnectURL(): Promise<string | null> {
  if (!isNativeMobile()) return null;
  return await Linking.getInitialURL();
}
