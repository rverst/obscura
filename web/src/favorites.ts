// Favourites: marking photos, and the filter that shows only those.

import { signal } from "@preact/signals";
import { ApiError, api, isUnauthorized, type Media } from "./api";
import { albumItems } from "./albums";
import { items, reloadTimeline } from "./state";
import { lightbox, lightboxScope } from "./viewer";

/**
 * Whether every view is narrowed to favourites.
 *
 * One signal for the whole app rather than per view: turning the filter on and
 * then navigating somewhere that quietly ignored it would look like the filter
 * had switched itself off.
 */
export const favoritesOnly = signal(false);

export const favoriteError = signal<string>("");

export async function setFavoritesOnly(on: boolean): Promise<void> {
  if (favoritesOnly.value === on) return;
  favoritesOnly.value = on;
  // The timeline is paged on the server, so the filter changes what the pages
  // contain: the loaded window and its cursor both have to go.
  await reloadTimeline();
}

/**
 * Marks or unmarks a photo, everywhere it is currently on screen.
 *
 * Applied locally first so the heart responds to the tap, and rolled back if
 * the server disagrees. The same record can be in the timeline, an open album
 * and the lightbox at once, and a heart that filled in one place but not
 * another would look like a bug in whichever place the user was watching.
 */
export async function setFavorite(id: number, favorite: boolean): Promise<void> {
  const before = {
    items: items.value,
    album: albumItems.value,
    scope: lightboxScope.value,
    open: lightbox.value,
  };
  apply(id, favorite);

  try {
    await api.setFavorite(id, favorite);
    // Unmarking while the filter is on removes it from what is being shown.
    if (!favorite && favoritesOnly.value) {
      items.value = items.value.filter((m) => m.id !== id);
      lightboxScope.value = lightboxScope.value.filter((m) => m.id !== id);
    }
  } catch (err) {
    items.value = before.items;
    albumItems.value = before.album;
    lightboxScope.value = before.scope;
    lightbox.value = before.open;
    favoriteError.value = messageOf(err);
  }
}

function apply(id: number, favorite: boolean): void {
  const mark = <T extends Media>(m: T): T => (m.id === id ? { ...m, favorite } : m);
  items.value = items.value.map(mark);
  albumItems.value = albumItems.value.map(mark);
  lightboxScope.value = lightboxScope.value.map(mark);
  if (lightbox.value?.id === id) lightbox.value = mark(lightbox.value);
}

/** Narrows a fully loaded list, for views the server does not page. */
export function filterFavorites<T extends Media>(list: T[]): T[] {
  return favoritesOnly.value ? list.filter((m) => m.favorite) : list;
}

function messageOf(err: unknown): string {
  // A lapsed session is reported centrally, by leaving for the login screen.
  // Recording it here too would outlive the session it belongs to.
  if (isUnauthorized(err)) return "";
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "something went wrong";
}
