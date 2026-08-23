// Albums: API calls and the client-side state for viewing and reordering them.

import { signal } from "@preact/signals";
import { ApiError, isUnauthorized, request, type Media } from "./api";
import { forgetInViewer } from "./viewer";

export interface Album {
  id: number;
  title: string;
  description: string;
  /** Absent at the top level. Albums nest: Croatia -> 2024, 2025. */
  parent_id?: number;
  /** This album's own photos. */
  item_count: number;
  /** Including everything nested inside it, which is what a tile shows. */
  total_count: number;
  cover_media_id?: number;
  /**
   * The picture to show for this album, worked out by the server: chosen by
   * hand if there is one, else the newest photo, else the best cover among the
   * sub-albums. A full record rather than an id, so the tile can build a
   * cacheable URL without another round trip.
   */
  cover?: Media;
  created_at: number;
}

/** A photo inside an album, carrying its position. */
export interface AlbumMedia extends Media {
  sort_key: string;
}

export const albumApi = {
  list: () => request<{ albums: Album[] }>("/api/albums"),
  create: (title: string, description = "", parentID: number | null = null) =>
    request<Album>("/api/albums", {
      method: "POST",
      body: JSON.stringify({ title, description, parent_id: parentID }),
    }),
  get: (id: number) => request<Album>(`/api/albums/${id}`),
  update: (id: number, title: string, description: string) =>
    request<void>(`/api/albums/${id}`, {
      method: "PATCH",
      body: JSON.stringify({ title, description }),
    }),
  remove: (id: number) =>
    request<{ deleted: number }>(`/api/albums/${id}`, { method: "DELETE" }),

  /** Breadcrumb, root first. */
  path: (id: number) =>
    request<{ path: { id: number; title: string }[] }>(`/api/albums/${id}/path`),

  /** Moves an album under another, or to the top level with null. */
  setParent: (id: number, parentID: number | null) =>
    request<void>(`/api/albums/${id}/parent`, {
      method: "PATCH",
      body: JSON.stringify({ parent_id: parentID }),
    }),

  /**
   * Places an album between two of its siblings.
   *
   * Neighbours rather than an index, for the same reason photos are moved that
   * way: an index describes a list that may have changed since it was drawn.
   */
  setPosition: (id: number, after: number | null, before: number | null) =>
    request<void>(`/api/albums/${id}/position`, {
      method: "PATCH",
      body: JSON.stringify({ after, before }),
    }),

  /** Pins a cover, or hands the choice back to the server with null. */
  setCover: (id: number, mediaID: number | null) =>
    request<void>(`/api/albums/${id}/cover`, {
      method: "PUT",
      body: JSON.stringify({ media_id: mediaID }),
    }),

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
/**
 * The breadcrumb for the open album, root first.
 *
 * Kept as its own signal rather than derived from `albums`, because the album
 * list is refetched often and the trail must not flicker or vanish while that
 * is in flight.
 */
export const albumPath = signal<{ id: number; title: string }[]>([]);
export const albumItems = signal<AlbumMedia[]>([]);
export const albumError = signal<string>("");
/** Edit mode is what unlocks reordering and the upload area. */
export const editing = signal(false);

/**
 * The album currently being dragged, or null.
 *
 * A signal rather than component state because the drag starts on a tile and
 * can end on a breadcrumb, and those are two components with no parent between
 * them that owns both.
 */
export const draggingAlbum = signal<number | null>(null);

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
    const [album, items, path] = await Promise.all([
      albumApi.get(id),
      albumApi.items(id),
      albumApi.path(id),
    ]);
    currentAlbum.value = album;
    albumItems.value = items.items ?? [];
    albumPath.value = path.path ?? [];
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

/** Goes back to the top level, where albums are browsed as tiles. */
export function openLibraryRoot(): void {
  albumError.value = "";
  editing.value = false;
  currentAlbum.value = null;
  albumItems.value = [];
  albumPath.value = [];
}

/**
 * The albums directly inside a parent, or the top level when null.
 *
 * Read from the flat list the server sends rather than fetched per level: a
 * self-hosted library has tens of albums, not thousands, and one request that
 * answers every level makes navigating instant.
 */
export function childAlbums(parentID: number | null): Album[] {
  return albums.value.filter((a) =>
    parentID === null ? a.parent_id === undefined : a.parent_id === parentID,
  );
}

/** The album an album sits in, or null at the top level. */
export function parentOf(id: number): number | null {
  const album = albums.value.find((a) => a.id === id);
  return album?.parent_id ?? null;
}

/**
 * The index a photo lands on when dropped beside another.
 *
 * moveItem takes the index in the list *after* the dragged photo is lifted out,
 * so every target behind it shifts down by one. Doing that arithmetic inline in
 * a drag handler is how off-by-ones get in: dropping "after" the tile you
 * started from is not the same index as dropping "after" the one before it.
 */
export function indexForDrop(
  items: { id: number }[],
  draggedID: number,
  targetIndex: number,
  side: "before" | "after",
): number {
  const from = items.findIndex((m) => m.id === draggedID);
  const raw = side === "after" ? targetIndex + 1 : targetIndex;
  return from >= 0 && from < raw ? raw - 1 : raw;
}

export async function createAlbum(
  title: string,
  parentID: number | null = null,
): Promise<Album | null> {
  try {
    const album = await albumApi.create(title, "", parentID);
    await loadAlbums();
    return album;
  } catch (err) {
    albumError.value = messageOf(err);
    return null;
  }
}

/**
 * Moves an album to a new parent.
 *
 * The server refuses a move that would make an album its own ancestor; that
 * error is worth showing rather than swallowing, because from the outside the
 * drag simply appears to have done nothing.
 */
export async function moveAlbum(id: number, parentID: number | null): Promise<void> {
  albumError.value = "";
  try {
    await albumApi.setParent(id, parentID);
    await loadAlbums();
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

/**
 * Reorders an album within its level.
 *
 * Optimistic like the photo reorder: the tiles are rearranged locally so the
 * drop lands where the pointer is, then the server is told. A rejection puts
 * the previous order back, because the next reload would anyway.
 */
export async function reorderAlbum(
  id: number,
  after: number | null,
  before: number | null,
): Promise<void> {
  albumError.value = "";
  const previous = albums.value;

  const moved = previous.find((a) => a.id === id);
  if (!moved) return;
  const rest = previous.filter((a) => a.id !== id);
  const at =
    after !== null
      ? rest.findIndex((a) => a.id === after) + 1
      : before !== null
        ? rest.findIndex((a) => a.id === before)
        : rest.length;
  albums.value = [...rest.slice(0, at), moved, ...rest.slice(at)];

  try {
    await albumApi.setPosition(id, after, before);
    await loadAlbums();
  } catch (err) {
    albums.value = previous;
    albumError.value = messageOf(err);
  }
}

export async function deleteAlbum(id: number): Promise<void> {
  try {
    await albumApi.remove(id);
    // Deleting takes the sub-albums with it, so anything below the deleted
    // album is gone too -- including, possibly, the one being looked at.
    const open = currentAlbum.value;
    if (open && (open.id === id || albumPath.value.some((a) => a.id === id))) {
      openLibraryRoot();
    }
    await loadAlbums();
  } catch (err) {
    albumError.value = messageOf(err);
  }
}

/**
 * Pins the open album's cover to one photo, or clears it back to automatic.
 *
 * Albums pick a cover by themselves -- the newest photo, or the best one from a
 * sub-album -- which is right until it is not. Clearing it is the same call with
 * null, so the automatic choice is never a door that only shuts once.
 */
export async function setAlbumCover(mediaID: number | null): Promise<void> {
  const album = currentAlbum.value;
  if (!album) return;
  albumError.value = "";
  try {
    await albumApi.setCover(album.id, mediaID);
    currentAlbum.value = { ...album, cover_media_id: mediaID ?? undefined };
    // The tiles carry the cover, so the list has to hear about it too.
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
  // A lapsed session is reported centrally, by leaving for the login screen.
  // Recording it here too would outlive the session it belongs to.
  if (isUnauthorized(err)) return "";
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "something went wrong";
}
