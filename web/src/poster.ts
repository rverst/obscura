// Choosing which frame stands for a video.

import { signal } from "@preact/signals";
import { api, isUnauthorized, type Media } from "./api";
import { albumItems, loadAlbums } from "./albums";
import { items } from "./state";
import { searchResults } from "./trash";
import { lightbox, lightboxScope } from "./viewer";

/** The video whose still is being cut, so the button can say so. */
export const posterBusy = signal<number | null>(null);

/** The last failure, tied to its video so it does not follow the arrow keys. */
export const posterError = signal<{ id: number; message: string } | null>(null);

/**
 * Makes the frame at atMs the video's thumbnail and preview.
 *
 * Not applied optimistically like a favourite: there is nothing to show until
 * the server has cut the frame. The reply carries new cache tokens, and
 * swapping them in everywhere is what makes each tile fetch the new still
 * instead of the one it has cached.
 */
export async function setPoster(id: number, atMs: number): Promise<void> {
  posterBusy.value = id;
  posterError.value = null;
  try {
    const updated = await api.setPoster(id, Math.max(0, Math.round(atMs)));
    // Only the stills changed. Merging keeps whatever else a list carries,
    // such as an album item's sort key.
    const swap = <T extends Media>(m: T): T =>
      m.id === id ? { ...m, has_thumb: updated.has_thumb, v: updated.v } : m;
    items.value = items.value.map(swap);
    albumItems.value = albumItems.value.map(swap);
    searchResults.value = searchResults.value.map(swap);
    lightboxScope.value = lightboxScope.value.map(swap);
    if (lightbox.value?.id === id) lightbox.value = swap(lightbox.value);
    // An album tile may show this video as its cover, and the tiles carry
    // their cover's tokens.
    void loadAlbums();
  } catch (err) {
    // A lapsed session is reported centrally, by leaving for the login screen.
    if (!isUnauthorized(err)) {
      posterError.value = {
        id,
        message: err instanceof Error ? err.message : "cannot set the thumbnail",
      };
    }
  } finally {
    posterBusy.value = null;
  }
}
