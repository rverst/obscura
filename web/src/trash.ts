// Trash and search state.

import { signal } from "@preact/signals";
import { api, ApiError, isUnauthorized, type Media, type TrashedMedia } from "./api";
import { albumItems, loadAlbums } from "./albums";
import { items, reloadTimeline } from "./state";
import { forgetInViewer } from "./viewer";

export const trashed = signal<TrashedMedia[]>([]);
export const trashError = signal<string>("");

export const searchQuery = signal<string>("");
export const searchResults = signal<Media[]>([]);
export const searching = signal(false);

export async function loadTrash(): Promise<void> {
  trashError.value = "";
  try {
    const res = await api.listTrash();
    trashed.value = res.items ?? [];
  } catch (err) {
    trashError.value = messageOf(err);
  }
}

/**
 * Moves a photo to the trash.
 *
 * A photo can be on screen in several places at once -- the timeline, an open
 * album, a list of search results -- and it has to leave all of them. Removing
 * it from only the timeline is why deleting from an album appeared to do
 * nothing until the page was reloaded.
 *
 * The album list is refetched too, so the counts on the tiles stop including a
 * photo that is now in the trash.
 */
export async function trashMedia(id: number): Promise<void> {
  const previous = {
    items: items.value,
    album: albumItems.value,
    results: searchResults.value,
  };
  items.value = previous.items.filter((m) => m.id !== id);
  albumItems.value = previous.album.filter((m) => m.id !== id);
  searchResults.value = previous.results.filter((m) => m.id !== id);
  forgetInViewer(id);
  try {
    await api.trash(id);
    await Promise.all([loadTrash(), loadAlbums()]);
  } catch (err) {
    items.value = previous.items;
    albumItems.value = previous.album;
    searchResults.value = previous.results;
    trashError.value = messageOf(err);
  }
}

export async function restoreMedia(id: number): Promise<void> {
  const previous = trashed.value;
  trashed.value = previous.filter((m) => m.id !== id);
  forgetInViewer(id);
  try {
    await api.restore(id);
    // The photo is back in the library and in whichever albums it was in, so
    // the counts and the timeline both need to hear about it.
    await loadAlbums();
    await reloadTimeline();
  } catch (err) {
    trashed.value = previous;
    trashError.value = messageOf(err);
  }
}

export async function purgeMedia(id: number): Promise<void> {
  const previous = trashed.value;
  trashed.value = previous.filter((m) => m.id !== id);
  forgetInViewer(id);
  try {
    await api.purge(id);
  } catch (err) {
    trashed.value = previous;
    trashError.value = messageOf(err);
  }
}

export async function emptyTrash(): Promise<void> {
  const previous = trashed.value;
  trashed.value = [];
  try {
    await api.emptyTrash();
  } catch (err) {
    trashed.value = previous;
    trashError.value = messageOf(err);
  }
}

/** Days left before a trashed photo is deleted for good. */
export function daysLeft(purgeAt: number): number {
  const ms = purgeAt * 1000 - Date.now();
  return Math.max(0, Math.ceil(ms / (24 * 60 * 60 * 1000)));
}

let searchToken = 0;

/**
 * Runs a search.
 *
 * Responses are tagged so a slow earlier request cannot overwrite the results
 * of a later one -- typing produces overlapping requests, and out-of-order
 * replies would show results for a query the box no longer contains.
 */
export async function runSearch(query: string): Promise<void> {
  const trimmed = query.trim();
  searchQuery.value = query;

  if (trimmed === "") {
    searchResults.value = [];
    searching.value = false;
    searchToken++; // cancel anything in flight
    return;
  }

  const token = ++searchToken;
  searching.value = true;
  try {
    const res = await api.search(trimmed);
    if (token !== searchToken) return;
    searchResults.value = res.items ?? [];
  } catch (err) {
    if (token !== searchToken) return;
    trashError.value = messageOf(err);
    searchResults.value = [];
  } finally {
    if (token === searchToken) searching.value = false;
  }
}

export function clearSearch(): void {
  searchQuery.value = "";
  searchResults.value = [];
  searching.value = false;
  searchToken++;
}

function messageOf(err: unknown): string {
  // A lapsed session is reported centrally, by leaving for the login screen.
  // Recording it here too would outlive the session it belongs to.
  if (isUnauthorized(err)) return "";
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "something went wrong";
}
