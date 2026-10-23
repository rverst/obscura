// Typed wrapper around the obscura HTTP API.
//
// Everything is same-origin and relies on the session cookie, which is HttpOnly
// and therefore invisible to this code. That is deliberate: a token in
// JavaScript is a token an XSS bug can steal.

export interface MediaExif {
  make?: string;
  model?: string;
  lens?: string;
  iso?: number;
  f_number?: number;
  exposure?: string;
  focal_length?: number;
}

/** Opaque per-derivative cache tokens; see mediaURL. */
export interface MediaVersions {
  thumb: string;
  preview: string;
  original: string;
}

export interface Media {
  id: number;
  kind: "photo" | "video";
  filename: string;
  mime: string;
  bytes: number;
  sha256: string;
  width: number;
  height: number;
  created_at: number;
  taken_at?: number;
  duration_ms?: number;
  has_thumb: boolean;
  favorite?: boolean;
  v?: MediaVersions;
  exif?: MediaExif;
  gps?: { lat: number; lon: number };
}

/** A photo in the trash, carrying its deletion deadline. */
export interface TrashedMedia extends Media {
  deleted_at: number;
  purge_at: number;
}

export interface Cursor {
  before: number;
  before_id: number;
}

export interface MediaPage {
  items: Media[];
  next?: Cursor;
  /** How many items the whole filtered listing holds, across every page. */
  total?: number;
}

/** Narrowings of the timeline, applied on the server. */
export interface ListFilter {
  favorite?: boolean;
  /** Only media that is in no album. */
  unfiled?: boolean;
}

export interface Status {
  initialized: boolean;
  authenticated: boolean;
  /** Whether to offer the passkey button at all; see the status handler. */
  passkeys: boolean;
}

/** One way to unlock the library: a password, a recovery code or a passkey. */
export interface UnlockMethod {
  id: number;
  kind: "password" | "recovery" | "passkey";
  label: string;
  created_at: number;
  last_used_at?: number;
}

export interface Me {
  username: string;
  unlock_via: UnlockMethod[];
  /** False when this address cannot do WebAuthn -- plain http, say. */
  passkeys_ready: boolean;
}

/** A WebAuthn ceremony as it comes off the wire, binary fields still base64url. */
export interface PasskeyChallenge {
  ceremony: string;
  options: { publicKey: Record<string, unknown> };
}

export interface PasskeyRegisterChallenge extends PasskeyChallenge {
  prf_salt: string;
}

export interface PasskeyLoginChallenge extends PasskeyChallenge {
  prf_salts: Record<string, string>;
}

/** Thrown for any non-2xx response, carrying the server's message. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/**
 * Told when the server says the session is gone.
 *
 * A callback registered by state.ts rather than an import of it: api.ts is the
 * bottom of the stack and everything else is built on it, so reaching upwards
 * from here would be a cycle.
 */
let onExpired: (() => void) | null = null;

export function whenSessionExpires(fn: () => void): void {
  onExpired = fn;
}

/**
 * Reports a response that came back unauthorised.
 *
 * Exported for the uploader, which speaks to the server with its own fetches --
 * it streams binary chunks and reads progress, which this module's JSON request
 * cannot do. It still has to notice a dead session like everything else.
 */
export function noteUnauthorized(status: number): void {
  if (status === 401) onExpired?.();
}

/**
 * Whether this failure is a session that is no longer there.
 *
 * The modules that run inside the gallery use it to keep quiet: leaving for the
 * login screen is the whole of the response, and recording "not logged in" as a
 * local error as well would leave that message sitting in a signal, to reappear
 * under the album grid the moment someone signs back in.
 *
 * The login screen's own errors deliberately do not use this. A wrong password
 * is answered with 401 too, and there it is the answer, not a session ending.
 */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401;
}

/**
 * Every request the app makes, and the one place a dead session is noticed.
 *
 * Exported because albums.ts had a byte-for-byte copy of this, which is
 * precisely why clicking an album after the session lapsed reported "not logged
 * in" as if it were an ordinary failure and left the app looking signed in --
 * the copy had never been taught what a 401 means.
 */
export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    // Cookies are same-origin anyway, but being explicit means this keeps
    // working if the app is ever served from a different host than the API.
    credentials: "same-origin",
  });

  if (resp.status === 204) return undefined as T;

  let body: unknown;
  try {
    body = await resp.json();
  } catch {
    body = null;
  }
  if (!resp.ok) {
    // Announced before it is thrown, so a caller that only knows how to show
    // the message still cannot leave the app sitting on a dead session. Signing
    // in returns a 401 for a wrong password too, which is why the handler --
    // not this function -- decides whether one means anything.
    noteUnauthorized(resp.status);
    const message =
      (body as { error?: string } | null)?.error ?? `request failed (${resp.status})`;
    throw new ApiError(resp.status, message);
  }
  return body as T;
}

export const api = {
  status: () => request<Status>("/api/status"),
  me: () => request<Me>("/api/me"),

  setup: (username: string, password: string) =>
    request<{ username: string; recovery_code: string; warning: string }>("/api/setup", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),

  login: (username: string, password: string) =>
    request<{ username: string }>("/api/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),

  loginWithRecovery: (username: string, code: string) =>
    request<{ username: string }>("/api/login/recovery", {
      method: "POST",
      body: JSON.stringify({ username, code }),
    }),

  logout: () => request<void>("/api/logout", { method: "POST" }),

  passkeyRegisterBegin: () =>
    request<PasskeyRegisterChallenge>("/api/passkeys/register/begin", {
      method: "POST",
    }),

  passkeyRegisterFinish: (body: {
    ceremony: string;
    label: string;
    unlock_key: string;
    credential: unknown;
  }) =>
    request<void>("/api/passkeys/register/finish", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  passkeyRegisterAbort: (ceremony: string) =>
    request<void>("/api/passkeys/register/abort", {
      method: "POST",
      body: JSON.stringify({ ceremony }),
    }),

  passkeyLoginBegin: (username?: string) =>
    request<PasskeyLoginChallenge>("/api/passkeys/login/begin", {
      method: "POST",
      body: JSON.stringify(username ? { username } : {}),
    }),

  passkeyLoginFinish: (body: {
    ceremony: string;
    unlock_key: string;
    credential: unknown;
  }) =>
    request<{ username: string }>("/api/passkeys/login/finish", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  removePasskey: (id: number) =>
    request<void>(`/api/passkeys/${id}`, { method: "DELETE" }),

  search: (query: string, limit = 100) => {
    const q = new URLSearchParams({ q: query, limit: String(limit) });
    return request<MediaPage>(`/api/media?${q}`);
  },

  trash: (id: number) => request<void>(`/api/media/${id}`, { method: "DELETE" }),
  restore: (id: number) => request<void>(`/api/media/${id}/restore`, { method: "POST" }),
  purge: (id: number) => request<void>(`/api/media/${id}/purge`, { method: "DELETE" }),
  listTrash: () => request<{ items: TrashedMedia[] }>("/api/trash"),
  emptyTrash: () => request<{ purged: number }>("/api/trash/empty", { method: "POST" }),

  listMedia: (cursor?: Cursor, limit = 100, filter: ListFilter = {}) => {
    const q = new URLSearchParams({ limit: String(limit) });
    if (cursor) {
      q.set("before", String(cursor.before));
      q.set("before_id", String(cursor.before_id));
    }
    // Filtered on the server so paging keeps working: sifting a page in the
    // browser would return short pages of unpredictable length and break the
    // "load more" trigger.
    if (filter.favorite) q.set("favorite", "1");
    if (filter.unfiled) q.set("unfiled", "1");
    return request<MediaPage>(`/api/media?${q}`);
  },

  /**
   * Marks or unmarks a photo.
   *
   * Names the end state rather than toggling. A toggle depends on what this
   * client believes is currently true, and two taps that race would land
   * wherever they land.
   */
  setFavorite: (id: number, favorite: boolean) =>
    request<{ favorite: boolean; favorited_at?: number }>(`/api/media/${id}/favorite`, {
      method: favorite ? "PUT" : "DELETE",
    }),

  getMedia: (id: number) => request<Media>(`/api/media/${id}`),

  /** Makes the frame at atMs the video's stills; returns the updated record. */
  setPoster: (id: number, atMs: number) =>
    request<Media>(`/api/media/${id}/poster`, {
      method: "PUT",
      body: JSON.stringify({ at_ms: atMs }),
    }),
};

/**
 * URL of a stored derivative.
 *
 * The version token matters. Media ids are sequential, so a bare
 * /api/media/1/thumb is a different photograph after the library is reset --
 * and a browser that cached it has no reason to ask again. Naming the blob in
 * the URL makes it genuinely immutable, so it can be cached hard and is still
 * always correct.
 */
export function mediaURL(
  media: Media | number,
  variant: "thumb" | "preview" | "original",
): string {
  if (typeof media === "number") {
    // No version to hand; the server falls back to revalidating.
    return `/api/media/${media}/${variant}`;
  }
  const token = media.v?.[variant];
  const base = `/api/media/${media.id}/${variant}`;
  return token ? `${base}?v=${encodeURIComponent(token)}` : base;
}
