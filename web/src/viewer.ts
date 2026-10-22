// Lightbox state.
//
// Its own module rather than part of state.ts because both the timeline and the
// album view need it, and albums.ts has to be able to close it when the photo
// being viewed is removed. Keeping it here means the dependencies stay one-way
// instead of state.ts and albums.ts importing each other.

import { signal } from "@preact/signals";
import type { Media, TrashedMedia } from "./api";

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

/**
 * Whether this record came out of the trash.
 *
 * Decided from the item itself rather than a separate "which view am I in"
 * signal: the deletion deadline only exists on a trashed record, so carrying
 * the answer with the data cannot disagree with it.
 */
export function isTrashed(media: Media): media is TrashedMedia {
  return typeof (media as TrashedMedia).purge_at === "number";
}

const INFO_KEY = "obscura.lightbox.info";

function loadInfoPinned(): boolean {
  try {
    // Shown unless it was explicitly turned off: the metadata is most of why
    // one opens a photo full-screen.
    return localStorage.getItem(INFO_KEY) !== "0";
  } catch {
    return true;
  }
}

/** Whether the lightbox shows its detail panel. Remembered across sessions. */
export const infoPinned = signal(loadInfoPinned());

export function toggleInfo(): void {
  infoPinned.value = !infoPinned.value;
  try {
    localStorage.setItem(INFO_KEY, infoPinned.value ? "1" : "0");
  } catch {
    // A browser that refuses storage still gets a working toggle; it just
    // forgets between visits.
  }
}

const VOLUME_KEY = "obscura.video.volume";

/** What a video player was last left at, so the next clip starts there too. */
export interface VideoVolume {
  volume: number;
  muted: boolean;
}

/**
 * The remembered volume, or null to leave the browser's default alone.
 *
 * Per device rather than on the server: the right level depends on the
 * speakers in front of you, not on the library.
 */
export function loadVolume(): VideoVolume | null {
  try {
    const raw = localStorage.getItem(VOLUME_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<VideoVolume>;
    if (typeof v.volume !== "number" || !(v.volume >= 0 && v.volume <= 1)) return null;
    return { volume: v.volume, muted: v.muted === true };
  } catch {
    return null;
  }
}

export function saveVolume(v: VideoVolume): void {
  try {
    localStorage.setItem(VOLUME_KEY, JSON.stringify(v));
  } catch {
    // As with the info panel: it works, it just forgets.
  }
}
