// Albums: API calls and the client-side state for viewing and reordering them.

import { signal } from "@preact/signals";
import { ApiError, type Media } from "./api";
import { forgetInViewer } from "./viewer";

export interface Album {
  id: number;
  title: string;
  description: string;
  item_count: number;
  cover_media_id?: number;
  created_at: number;
}

/** A photo inside an album, carrying its position. */
export interface AlbumMedia extends Media {
  sort_key: string;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    credentials: "same-origin",
  });
  if (resp.status === 204) return undefined as T;

  let body: unknown = null;
  try {
    body = await resp.json();
  } catch {
    body = null;
  }
  if (!resp.ok) {
    throw new ApiError(
      resp.status,
      (body as { error?: string } | null)?.error ?? `request failed (${resp.status})`,
    );
  }
  return body as T;
}

export const albumApi = {
  list: () => request<{ albums: Album[] }>("/api/albums"),
  create: (title: string, description = "") =>
    request<Album>("/api/albums", {
      method: "POST",
      body: JSON.stringify({ title, description }),
    }),
  get: (id: number) => request<Album>(`/api/albums/${id}`),
  update: (id: number, title: string, description: string) =>
    request<void>(`/api/albums/${id}`, {
      method: "PATCH",
      body: JSON.stringify({ title, description }),
    }),
  remove: (id: number) => request<void>(`/api/albums/${id}`, { method: "DELETE" }),

  items: (id: number) => request<{ items: AlbumMedia[] }>(`/api/albums/${id}/items`),
  add: (id: number, mediaIDs: number[]) =>
    request<{ added: number; requested: number }>(`/api/albums/${id}/items`, {
      method: "POST",
      body: JSON.stringify({ media_ids: mediaIDs }),
    }),
  removeItem: (id: number, mediaID: number) =>
    request<void>(`/api/albums/${id}/items/${mediaID}`, { method: "DELETE" }),

  /**
   * Moves a photo between two neighbours.
   *
   * Neighbours rather than an index, because an index means nothing if the
   * album changed since it was rendered.
   */
  move: (id: number, mediaID: number, after: number | null, before: number | null) =>
    request<{ sort_key: string }>(`/api/albums/${id}/items/${mediaID}/position`, {
      method: "PATCH",
      body: JSON.stringify({ after, before }),
    }),
};

// --- state ------------------------------------------------------------------

export const albums = signal<Album[]>([]);
export const currentAlbum = signal<Album | null>(null);
export const albumItems = signal<AlbumMedia[]>([]);
export const albumError = signal<string>("");
/** Edit mode is what unlocks reordering and the upload area. */
export const editing = signal(false);

export async function loadAlbums(): Promise<void> {
  try {
    const res = await albumApi.list();
    albums.value = res.albums ?? [];
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

export async function openAlbum(id: number): Promise<void> {
  albumError.value = "";
  editing.value = false;
  try {
    const [album, items] = await Promise.all([albumApi.get(id), albumApi.items(id)]);
    currentAlbum.value = album;
    albumItems.value = items.items ?? [];
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

export async function createAlbum(title: string): Promise<Album | null> {
  try {
    const album = await albumApi.create(title);
    await loadAlbums();
    return album;
  } catch (err) {
    albumError.value = messageOf(err);
    return null;
  }
}

export async function deleteAlbum(id: number): Promise<void> {
  try {
    await albumApi.remove(id);
    if (currentAlbum.value?.id === id) {
      currentAlbum.value = null;
      albumItems.value = [];
    }
    await loadAlbums();
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

export async function removeFromAlbum(mediaID: number): Promise<void> {
  const album = currentAlbum.value;
  if (!album) return;
  const previous = albumItems.value;
  albumItems.value = previous.filter((m) => m.id !== mediaID);
  // Leaving it open would show a photo that is no longer part of what is being
  // looked at, and stepping away from it with the arrows would be incoherent.
  forgetInViewer(mediaID);
  try {
    await albumApi.removeItem(album.id, mediaID);
    await loadAlbums();
  } catch (err) {
    albumItems.value = previous;
    albumError.value = messageOf(err);
  }
}

/**
 * Moves a photo to a new index, optimistically.
 *
 * The list is reordered locally first so the drag feels immediate, then the
 * server is told. On failure the previous order is restored -- silently keeping
 * a local order the server rejected would be worse than a visible snap back,
 * because the next reload would undo it anyway.
 */
export async function moveItem(mediaID: number, toIndex: number): Promise<void> {
  const album = currentAlbum.value;
  if (!album) return;

  const previous = albumItems.value;
  const from = previous.findIndex((m) => m.id === mediaID);
  if (from < 0 || from === toIndex) return;

  const next = [...previous];
  const [moved] = next.splice(from, 1);
  if (!moved) return;
  next.splice(toIndex, 0, moved);
  albumItems.value = next;

  const at = next.findIndex((m) => m.id === mediaID);
  const after = at > 0 ? next[at - 1]!.id : null;
  const before = at < next.length - 1 ? next[at + 1]!.id : null;

  try {
    const res = await albumApi.move(album.id, mediaID, after, before);
    // Keep the authoritative key, so a later move computed from it is correct.
    albumItems.value = albumItems.value.map((m) =>
      m.id === mediaID ? { ...m, sort_key: res.sort_key } : m,
    );
  } catch (err) {
    albumItems.value = previous;
    albumError.value = messageOf(err);
  }
}

/**
 * Adds media to a named album.
 *
 * Takes the album explicitly rather than using whichever one is open, so an
 * upload started in one album cannot land in another because the user navigated
 * while it was in flight.
 */
export async function addToAlbum(albumID: number, mediaIDs: number[]): Promise<void> {
  if (mediaIDs.length === 0) return;
  try {
    await albumApi.add(albumID, mediaIDs);
    if (currentAlbum.value?.id === albumID) {
      await refreshAlbumItems();
    }
    await loadAlbums();
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

/**
 * Reloads the open album's contents without touching edit mode.
 *
 * openAlbum deliberately leaves edit mode, which is right when navigating but
 * wrong after an upload: it dropped the user out of editing mid-batch.
 */
export async function refreshAlbumItems(): Promise<void> {
  const album = currentAlbum.value;
  if (!album) return;
  try {
    const [meta, items] = await Promise.all([
      albumApi.get(album.id),
      albumApi.items(album.id),
    ]);
    currentAlbum.value = meta;
    albumItems.value = items.items ?? [];
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "something went wrong";
}
