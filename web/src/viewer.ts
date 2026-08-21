// Lightbox state.
//
// Its own module rather than part of state.ts because both the timeline and the
// album view need it, and albums.ts has to be able to close it when the photo
// being viewed is removed. Keeping it here means the dependencies stay one-way
// instead of state.ts and albums.ts importing each other.

import { signal } from "@preact/signals";
import type { Media } from "./api";

/** The lightbox is open on this item, or closed when null. */
export const lightbox = signal<Media | null>(null);

/**
 * The collection the lightbox is browsing.
 *
 * Arrow keys have to walk the set the photo was opened from. Browsing the whole
 * library from inside an album was not merely surprising -- a photo outside the
 * loaded timeline window had no index there at all, so the arrows stopped
 * working entirely.
 */
export const lightboxScope = signal<Media[]>([]);

export function openLightbox(media: Media, scope: Media[]): void {
  lightboxScope.value = scope;
  lightbox.value = media;
}

export function closeLightbox(): void {
  lightbox.value = null;
  lightboxScope.value = [];
}

/** Drops an item from the current scope, closing the lightbox if it was on it. */
export function forgetInViewer(mediaID: number): void {
  if (lightbox.value?.id === mediaID) closeLightbox();
  lightboxScope.value = lightboxScope.value.filter((m) => m.id !== mediaID);
}
