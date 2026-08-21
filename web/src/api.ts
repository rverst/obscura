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
  exif?: MediaExif;
  gps?: { lat: number; lon: number };
}

export interface Cursor {
  before: number;
  before_id: number;
}

export interface MediaPage {
  items: Media[];
  next?: Cursor;
}

export interface Status {
  initialized: boolean;
  authenticated: boolean;
}

export interface Me {
  username: string;
  unlock_via: { id: number; kind: string; label: string; created_at: number }[];
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
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

  listMedia: (cursor?: Cursor, limit = 100) => {
    const q = new URLSearchParams({ limit: String(limit) });
    if (cursor) {
      q.set("before", String(cursor.before));
      q.set("before_id", String(cursor.before_id));
    }
    return request<MediaPage>(`/api/media?${q}`);
  },

  getMedia: (id: number) => request<Media>(`/api/media/${id}`),
};

/** URL of a stored derivative. Content is immutable, so these cache well. */
export function mediaURL(id: number, variant: "thumb" | "preview" | "original"): string {
  return `/api/media/${id}/${variant}`;
}
