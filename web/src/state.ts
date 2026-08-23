// Application state.
//
// Signals rather than a store library: the whole app is a gallery and a
// lightbox, and a reducer indirection would be more machinery than the problem
// needs on a device where the bundle size is felt.

import { signal } from "@preact/signals";
import { api, whenSessionExpires, type Cursor, type Media, ApiError } from "./api";
import { albums, currentAlbum, albumItems, loadAlbums } from "./albums";
import { closeLightbox } from "./viewer";
import { favoritesOnly } from "./favorites";
import { passkeysAvailable, signInWithPasskey } from "./passkey";
import { navigate, ROOT, startRouting } from "./router";

export type Screen = "loading" | "setup" | "login" | "gallery";

/**
 * Which part of the library the main area is showing.
 *
 * "album" covers both the top level and an open album: with no album selected
 * it is the tile browser, and inside one it is that album's sub-albums and
 * photos. They were two screens joined by a sidebar; they are one now.
 */
export type View = "all" | "album" | "trash" | "search";

export const screen = signal<Screen>("loading");
// Albums are the home screen; "All photos" is one tile among them.
export const view = signal<View>("album");
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
    // Last, so the address bar decides what is shown -- and only once there is
    // a library for it to point into.
    startRouting();
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
  toLoginScreen();
  // Back to the front door. Locking the library and having the next person to
  // sign in land in the album the last one was looking at is not what "lock"
  // is meant to mean.
  navigate(ROOT, { replace: true });
  await refreshUnlockOffer();
}

/**
 * The session lapsed while the app was sitting open.
 *
 * Every request reports a 401 here, so any click at all -- an album, the trash,
 * a favourite -- lands on the login screen instead of an error message beside a
 * library that is no longer there. Only one request had ever noticed, the
 * timeline's, and even that left the username and the loaded photographs
 * behind.
 *
 * Unlike locking the library by hand this leaves the address alone, so signing
 * back in returns to the album that was open rather than to the front door. It
 * is the same person coming back to their own window.
 */
function sessionExpired(): void {
  // Signing in with the wrong password is a 401 too. Anywhere but the gallery,
  // a 401 is an answer to a question that was asked, not a session going away.
  // It also collapses the storm when several requests fail at once: the first
  // one leaves the gallery and the rest fall out here.
  if (screen.value !== "gallery") return;
  toLoginScreen();
  authError.value = "Your session expired. Please sign in again.";
  void refreshUnlockOffer();
}

whenSessionExpires(sessionExpired);

/** Drops everything the signed-in session was holding. */
function toLoginScreen(): void {
  resetLibrary();
  username.value = "";
  // Whatever the last screen had to say about signing in is about a session
  // that is over. sessionExpired puts its own message back afterwards.
  authError.value = "";
  screen.value = "login";
}

/**
 * Asks again what the login screen should offer.
 *
 * A passkey may have been added or removed during the session that is ending.
 */
async function refreshUnlockOffer(): Promise<void> {
  try {
    const s = await api.status();
    passkeyOffered.value = s.passkeys && passkeysAvailable();
  } catch {
    passkeyOffered.value = false;
  }
}

async function enterGallery(): Promise<void> {
  resetLibrary();
  screen.value = "gallery";
  await Promise.all([loadMore(), loadAlbums()]);
  // Nothing on the way in touches the URL, so a link followed while locked is
  // still sitting in the address bar -- and this is where it is honoured.
  startRouting();
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
    const page = await api.listMedia(cursor, 100, favoritesOnly.value);
    const fresh = page.items ?? [];
    items.value = [...items.value, ...fresh];
    cursor = page.next;
    // No cursor, or a page that added nothing, means the end. Checking both
    // guards against an empty last page looping forever.
    if (!page.next || fresh.length === 0) exhausted = true;
  } catch (err) {
    // A lapsed session has already been dealt with centrally; saying so again
    // here would put "not logged in" under the login form.
    if (!(err instanceof ApiError && err.status === 401)) {
      loadError.value = messageOf(err);
    }
  } finally {
    loading.value = false;
  }
}

/**
 * Throws away the loaded timeline and fetches it again from the top.
 *
 * Needed wherever the server's answer would change underneath the cursor --
 * restoring from the trash, or switching the favourites filter. Patching the
 * loaded window instead would leave the cursor pointing into a sequence that
 * no longer exists, and paging would skip or repeat.
 */
export async function reloadTimeline(): Promise<void> {
  items.value = [];
  cursor = undefined;
  exhausted = false;
  await loadMore();
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
