// Application state.
//
// Signals rather than a store library: the whole app is a gallery and a
// lightbox, and a reducer indirection would be more machinery than the problem
// needs on a device where the bundle size is felt.

import { signal } from "@preact/signals";
import { api, type Cursor, type Media, ApiError } from "./api";
import { albums, currentAlbum, albumItems, loadAlbums } from "./albums";
import { closeLightbox } from "./viewer";
import { passkeysAvailable, signInWithPasskey } from "./passkey";

export type Screen = "loading" | "setup" | "login" | "gallery";

/** Which part of the library the main area is showing. */
export type View = "all" | "album" | "trash" | "search";

export const screen = signal<Screen>("loading");
export const view = signal<View>("all");
export const username = signal<string>("");
export const authError = signal<string>("");

/** Shown once after setup, and never retrievable again. */
export const recoveryCode = signal<string | null>(null);

/**
 * Whether the login screen offers a passkey.
 *
 * Both halves have to hold: the server must have one registered, and this
 * browser must be able to use it. A button that leads to a dead end is worse
 * than no button.
 */
export const passkeyOffered = signal(false);

export const items = signal<Media[]>([]);
export const loading = signal(false);
export const loadError = signal<string>("");
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
      passkeyOffered.value = s.passkeys && passkeysAvailable();
      screen.value = "login";
      return;
    }
    const me = await api.me();
    username.value = me.username;
    screen.value = "gallery";
    await Promise.all([loadMore(), loadAlbums()]);
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

export async function loginWithRecovery(
  user: string,
  code: string,
): Promise<void> {
  authError.value = "";
  try {
    const res = await api.loginWithRecovery(user, code);
    username.value = res.username;
    await enterGallery();
  } catch (err) {
    authError.value = messageOf(err);
  }
}

/**
 * Signs in with a passkey.
 *
 * No username: a passkey already identifies its owner, so asking first would
 * be a step that buys nothing.
 */
export async function loginWithPasskey(): Promise<void> {
  authError.value = "";
  try {
    username.value = await signInWithPasskey();
    await enterGallery();
  } catch (err) {
    // Dismissing the system dialog raises NotAllowedError. That is someone
    // changing their mind, not something to report as a failure.
    if (err instanceof DOMException && err.name === "NotAllowedError") return;
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
  // Ask again what the login screen should offer: a passkey may have been
  // added or removed during the session that is ending.
  try {
    const s = await api.status();
    passkeyOffered.value = s.passkeys && passkeysAvailable();
  } catch {
    passkeyOffered.value = false;
  }
  screen.value = "login";
}

async function enterGallery(): Promise<void> {
  resetLibrary();
  screen.value = "gallery";
  view.value = "all";
  await Promise.all([loadMore(), loadAlbums()]);
}

function resetLibrary(): void {
  albums.value = [];
  currentAlbum.value = null;
  albumItems.value = [];
  items.value = [];
  cursor = undefined;
  exhausted = false;
  loadError.value = "";
  closeLightbox();
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

/**
 * Places a freshly uploaded item into the loaded window.
 *
 * Not simply prepended: the timeline is ordered by capture time, and a photo
 * taken in 2009 but uploaded today belongs in 2009. If it sorts older than
 * anything loaded so far it is dropped here rather than shown in the wrong
 * place -- it will arrive on its own once the user scrolls that far, or after
 * the next reload.
 */
export function insertMedia(m: Media): void {
  const current = items.value;
  if (current.some((existing) => existing.id === m.id)) return;

  const key = (x: Media) => x.taken_at ?? x.created_at;
  const at = key(m);

  const last = current[current.length - 1];
  if (!exhausted && last && at < key(last)) return;

  const idx = current.findIndex((existing) => key(existing) <= at);
  if (idx < 0) {
    items.value = [...current, m];
    return;
  }
  items.value = [...current.slice(0, idx), m, ...current.slice(idx)];
}

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "something went wrong";
}
