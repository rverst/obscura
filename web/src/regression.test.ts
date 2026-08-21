// Tests for the four problems reported after M4. Each one reproduces the
// reported symptom rather than the fix, so a regression would show up as the
// same user-visible behaviour returning.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { albumItems, currentAlbum, removeFromAlbum, type AlbumMedia } from "./albums";
import type { Media } from "./api";
import { items } from "./state";
import { enqueue, tasks } from "./upload";
import { closeLightbox, lightbox, lightboxScope, openLightbox } from "./viewer";

function media(id: number): Media {
  return {
    id,
    kind: "photo",
    filename: `p${id}.jpg`,
    mime: "image/jpeg",
    bytes: 10,
    sha256: "",
    width: 100,
    height: 100,
    created_at: 1000,
    has_thumb: true,
  };
}

function albumMedia(id: number, sortKey: string): AlbumMedia {
  return { ...media(id), sort_key: sortKey };
}

// Records which album each add landed in.
let adds: { albumID: number; mediaIDs: number[] }[] = [];
let nextMediaID = 100;

function fakeServer() {
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const json = (b: unknown, status = 200) =>
      new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

    const addMatch = /^\/api\/albums\/(\d+)\/items$/.exec(path);
    if (addMatch && method === "POST") {
      const body = JSON.parse(String(init!.body)) as { media_ids: number[] };
      adds.push({ albumID: Number(addMatch[1]), mediaIDs: body.media_ids });
      return json({ added: body.media_ids.length, requested: body.media_ids.length });
    }
    if (/^\/api\/albums\/\d+\/items$/.test(path)) return json({ items: [] });
    if (/^\/api\/albums\/\d+$/.test(path)) {
      return json({ id: 1, title: "A", description: "", item_count: 0, created_at: 0 });
    }
    if (path === "/api/albums") return json({ albums: [] });
    if (path === "/api/media/check") return json({ exists: false });
    if (path === "/api/uploads" && method === "POST") return json({ upload_id: "u", offset: 0 }, 201);
    if (path.endsWith("/finish")) return json({ duplicate: false, media: media(nextMediaID++) }, 201);
    if (method === "PATCH") return json({ offset: 10 });
    return json({});
  };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 300; i++) {
    const pending = tasks.value.some((t) =>
      ["queued", "hashing", "uploading", "processing"].includes(t.status),
    );
    if (!pending && tasks.value.length > 0) return;
    await new Promise((r) => setTimeout(r, 10));
  }
}

beforeEach(() => {
  adds = [];
  nextMediaID = 100;
  tasks.value = [];
  items.value = [];
  albumItems.value = [];
  currentAlbum.value = null;
  closeLightbox();
  vi.stubGlobal("fetch", fakeServer());
});

afterEach(() => vi.unstubAllGlobals());

function file(name: string): File {
  return new File([new Uint8Array(10)], name, { type: "image/jpeg" });
}

describe("uploads are filed into the album they were queued for", () => {
  // Reported: creating a second album and uploading to it put the first
  // album's photos into it as well.
  it("does not re-file a previous album's uploads into the next one", async () => {
    enqueue([file("first.jpg")], 1);
    await settle();
    expect(adds).toEqual([{ albumID: 1, mediaIDs: [100] }]);

    // Second album. The task list is global and still holds the finished
    // upload above; it must not be filed again.
    enqueue([file("second.jpg")], 2);
    await settle();

    expect(adds).toEqual([
      { albumID: 1, mediaIDs: [100] },
      { albumID: 2, mediaIDs: [101] },
    ]);
  });

  it("files nothing when queued outside an album", async () => {
    enqueue([file("loose.jpg")]);
    await settle();
    expect(adds).toHaveLength(0);
  });

  it("keeps the destination even if the open album changes mid-upload", async () => {
    currentAlbum.value = { id: 1, title: "A", description: "", item_count: 0, created_at: 0 };
    enqueue([file("x.jpg")], 1);
    // Navigating away while it is in flight must not redirect the upload.
    currentAlbum.value = { id: 2, title: "B", description: "", item_count: 0, created_at: 0 };
    await settle();
    expect(adds).toEqual([{ albumID: 1, mediaIDs: [100] }]);
  });
});

describe("lightbox browses the collection it was opened from", () => {
  // Reported: a photo removed from an album was still visible in the lightbox.
  it("closes when the photo being viewed is removed from the album", async () => {
    currentAlbum.value = { id: 1, title: "A", description: "", item_count: 2, created_at: 0 };
    const album = [albumMedia(1, "a0"), albumMedia(2, "a1")];
    albumItems.value = album;
    openLightbox(album[0]!, album);

    await removeFromAlbum(1);

    expect(lightbox.value).toBeNull();
    expect(albumItems.value.map((m) => m.id)).toEqual([2]);
  });

  it("drops a removed photo from the scope without closing on another", async () => {
    currentAlbum.value = { id: 1, title: "A", description: "", item_count: 3, created_at: 0 };
    const album = [albumMedia(1, "a0"), albumMedia(2, "a1"), albumMedia(3, "a2")];
    albumItems.value = album;
    openLightbox(album[2]!, album);

    await removeFromAlbum(1);

    // Still viewing photo 3, but photo 1 must no longer be reachable with the
    // arrow keys.
    expect(lightbox.value?.id).toBe(3);
    expect(lightboxScope.value.map((m) => m.id)).toEqual([2, 3]);
  });

  it("browses the album, not the whole timeline", () => {
    items.value = [media(1), media(2), media(3), media(4), media(5)];
    const album = [albumMedia(4, "a0"), albumMedia(2, "a1")];
    openLightbox(album[0]!, album);

    // Opened from an album, so the scope is the album -- not the five photos
    // that happen to be loaded in the timeline.
    expect(lightboxScope.value.map((m) => m.id)).toEqual([4, 2]);
  });

  it("finds an index for a photo outside the loaded timeline window", () => {
    // The timeline has only pages one and two loaded; the album holds an older
    // photo that is not among them. Browsing the timeline would give index -1
    // and break the arrows entirely.
    items.value = [media(1), media(2)];
    const album = [albumMedia(99, "a0")];
    openLightbox(album[0]!, album);
    expect(lightboxScope.value.findIndex((m) => m.id === 99)).toBe(0);
  });
});
