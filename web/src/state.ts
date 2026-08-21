// Application state.
//
// Signals rather than a store library: the whole app is a gallery and a
// lightbox, and a reducer indirection would be more machinery than the problem
// needs on a device where the bundle size is felt.

import { signal } from "@preact/signals";
import { api, type Cursor, type Media, ApiError } from "./api";

export type Screen = "loading" | "setup" | "login" | "gallery";

export const screen = signal<Screen>("loading");
export const username = signal<string>("");
export const authError = signal<string>("");

/** Shown once after setup, and never retrievable again. */
export const recoveryCode = signal<string | null>(null);

export const items = signal<Media[]>([]);
export const loading = signal(false);
export const loadError = signal<string>("");
/** The lightbox is open on this item, or closed when null. */
export const lightbox = signal<Media | null>(null);

let cursor: Cursor | undefined;
let exhausted = false;

/** Decides which screen to show on startup. */
export async function boot(): Promise<void> {
  try {
    const s = await api.status();
    if (!s.initialized) {
      screen.value = "setup";
      return;
    }
    if (!s.authenticated) {
      screen.value = "login";
      return;
    }
    const me = await api.me();
    username.value = me.username;
    screen.value = "gallery";
    await loadMore();
  } catch (err) {
    authError.value = messageOf(err);
    screen.value = "login";
  }
}

export async function login(user: string, password: string): Promise<void> {
  authError.value = "";
  try {
    const res = await api.login(user, password);
    username.value = res.username;
    await enterGallery();
  } catch (err) {
    authError.value = messageOf(err);
  }
}

export async function loginWithRecovery(user: string, code: string): Promise<void> {
  authError.value = "";
  try {
    const res = await api.loginWithRecovery(user, code);
    username.value = res.username;
    await enterGallery();
  } catch (err) {
    authError.value = messageOf(err);
  }
}

export async function setup(user: string, password: string): Promise<void> {
  authError.value = "";
  try {
    const res = await api.setup(user, password);
    username.value = res.username;
    // Held in memory only and shown once. Persisting it anywhere would
    // undermine the point of a recovery code the server does not keep either.
    recoveryCode.value = res.recovery_code;
    await enterGallery();
  } catch (err) {
    authError.value = messageOf(err);
  }
}

export async function logout(): Promise<void> {
  await api.logout().catch(() => undefined);
  resetLibrary();
  username.value = "";
  screen.value = "login";
}

async function enterGallery(): Promise<void> {
  resetLibrary();
  screen.value = "gallery";
  await loadMore();
}

function resetLibrary(): void {
  items.value = [];
  cursor = undefined;
  exhausted = false;
  loadError.value = "";
  lightbox.value = null;
}

/** Fetches the next page. Safe to call repeatedly; concurrent calls collapse. */
export async function loadMore(): Promise<void> {
  if (loading.value || exhausted) return;
  loading.value = true;
  loadError.value = "";
  try {
    const page = await api.listMedia(cursor);
    const fresh = page.items ?? [];
    items.value = [...items.value, ...fresh];
    cursor = page.next;
    // No cursor, or a page that added nothing, means the end. Checking both
    // guards against an empty last page looping forever.
    if (!page.next || fresh.length === 0) exhausted = true;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      screen.value = "login";
      return;
    }
    loadError.value = messageOf(err);
  } finally {
    loading.value = false;
  }
}

export function hasMore(): boolean {
  return !exhausted;
}

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "something went wrong";
}
