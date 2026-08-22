// Unlock methods: what can open the library, and adding or removing one.

import { signal } from "@preact/signals";
import { ApiError, api, type UnlockMethod } from "./api";
import {
  PasskeyUnsupported,
  enrollPasskey,
  passkeysAvailable,
} from "./passkey";

export const securityOpen = signal(false);
export const unlockMethods = signal<UnlockMethod[]>([]);
export const securityError = signal("");
export const securityBusy = signal(false);

/**
 * Whether this browser and address can enroll a passkey.
 *
 * Two separate conditions: the server has to be reachable at an address the
 * browser considers secure, and the browser has to speak WebAuthn at all.
 * Neither is worth a button on its own.
 */
export const canEnroll = signal(false);

export async function openSecurity(): Promise<void> {
  securityOpen.value = true;
  securityError.value = "";
  await refreshMethods();
}

export function closeSecurity(): void {
  securityOpen.value = false;
  securityError.value = "";
}

export async function refreshMethods(): Promise<void> {
  try {
    const me = await api.me();
    unlockMethods.value = me.unlock_via;
    canEnroll.value = me.passkeys_ready && passkeysAvailable();
  } catch (err) {
    securityError.value = messageOf(err);
  }
}

export async function addPasskey(label: string): Promise<boolean> {
  securityError.value = "";
  securityBusy.value = true;
  try {
    await enrollPasskey(label.trim() || "Passkey");
    await refreshMethods();
    return true;
  } catch (err) {
    // Dismissing the system dialog is a decision, not an error to report back.
    if (err instanceof DOMException && err.name === "NotAllowedError")
      return false;
    securityError.value = messageOf(err);
    return false;
  } finally {
    securityBusy.value = false;
  }
}

export async function removePasskey(id: number): Promise<void> {
  securityError.value = "";
  try {
    await api.removePasskey(id);
    await refreshMethods();
  } catch (err) {
    securityError.value = messageOf(err);
  }
}

function messageOf(err: unknown): string {
  // A device that cannot produce key material has its own message, and it is
  // the only one here that tells the user what to do instead.
  if (err instanceof PasskeyUnsupported) return err.message;
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "something went wrong";
}
