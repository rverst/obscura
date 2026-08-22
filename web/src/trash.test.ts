import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Media, TrashedMedia } from "./api";
import { items } from "./state";
import {
  clearSearch,
  daysLeft,
  purgeMedia,
  restoreMedia,
  runSearch,
  searching,
  searchResults,
  trashed,
  trashMedia,
} from "./trash";
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

function trashedMedia(id: number, daysFromNow: number): TrashedMedia {
  const now = Math.floor(Date.now() / 1000);
  return {
    ...media(id),
    deleted_at: now,
    purge_at: now + daysFromNow * 24 * 60 * 60,
  };
}

let calls: string[] = [];
let failNext = false;
/** Resolves pending search responses in the order the test chooses. */
let deferred: { resolve: () => void; query: string }[] = [];

beforeEach(() => {
  calls = [];
  failNext = false;
  deferred = [];
  items.value = [];
  trashed.value = [];
  searchResults.value = [];
  closeLightbox();

  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    calls.push(`${method} ${path}`);
    const json = (b: unknown, status = 200) =>
      new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

    if (failNext) return json({ error: "nope" }, 500);

    if (path.startsWith("/api/media?q=")) {
      const query = decodeURIComponent(new URL(path, "http://x").searchParams.get("q") ?? "");
      // Held open so the test can decide the order replies arrive in.
      await new Promise<void>((resolve) => deferred.push({ resolve, query }));
      return json({ items: [{ ...media(query.length), filename: `${query}.jpg` }], query });
    }
    if (path === "/api/trash") return json({ items: trashed.value });
    return new Response(null, { status: 204 });
  });
});

afterEach(() => vi.unstubAllGlobals());

describe("trash", () => {
  it("removes the photo from the timeline straight away", async () => {
    items.value = [media(1), media(2)];
    const pending = trashMedia(1);
    // Waiting for the round trip would make deleting feel broken.
    expect(items.value.map((m) => m.id)).toEqual([2]);
    await pending;
  });

  it("closes the lightbox when the photo being viewed is deleted", async () => {
    const list = [media(1), media(2)];
    items.value = list;
    openLightbox(list[0]!, list);
    await trashMedia(1);
    expect(lightbox.value).toBeNull();
  });

  // Deleting a photo other than the one on screen must leave the viewer open
  // but must not leave the deleted one reachable with the arrow keys.
  it("drops a deleted photo from the viewer without closing it", async () => {
    const list = [media(1), media(2), media(3)];
    items.value = list;
    openLightbox(list[2]!, list);
    await trashMedia(1);
    expect(lightbox.value?.id).toBe(3);
    expect(lightboxScope.value.map((m) => m.id)).toEqual([2, 3]);
  });

  it("puts the photo back if the server refuses", async () => {
    items.value = [media(1), media(2)];
    failNext = true;
    await trashMedia(1);
    expect(items.value.map((m) => m.id)).toEqual([1, 2]);
  });

  it("restores and purges optimistically", async () => {
    trashed.value = [trashedMedia(1, 30), trashedMedia(2, 30)];
    await restoreMedia(1);
    expect(trashed.value.map((m) => m.id)).toEqual([2]);

    await purgeMedia(2);
    expect(trashed.value).toHaveLength(0);
    expect(calls).toContain("DELETE /api/media/2/purge");
  });

  // Purging is the irreversible step and must use its own path, never the one
  // that merely moves a photo to the trash.
  it("purges through a separate endpoint", async () => {
    trashed.value = [trashedMedia(1, 30)];
    await purgeMedia(1);
    expect(calls.some((c) => c === "DELETE /api/media/1")).toBe(false);
    expect(calls).toContain("DELETE /api/media/1/purge");
  });

  it("counts the days left from the server's deadline", () => {
    expect(daysLeft(Math.floor(Date.now() / 1000) + 10 * 86400)).toBe(10);
    // Already overdue: never negative.
    expect(daysLeft(Math.floor(Date.now() / 1000) - 86400)).toBe(0);
  });
});

describe("search", () => {
  const flush = () => {
    const pending = [...deferred];
    deferred = [];
    for (const d of pending) d.resolve();
  };

  it("returns results for the query", async () => {
    const p = runSearch("holiday");
    flush();
    await p;
    expect(searchResults.value[0]?.filename).toBe("holiday.jpg");
    expect(searching.value).toBe(false);
  });

  // Typing produces overlapping requests. An earlier reply arriving late must
  // not replace the results of the query currently in the box.
  it("ignores a stale response that arrives after a newer one", async () => {
    const slow = runSearch("cr");
    const slowPending = deferred.splice(0, 1);
    const fast = runSearch("crete");
    flush(); // the newer query answers first
    await fast;
    expect(searchResults.value[0]?.filename).toBe("crete.jpg");

    for (const d of slowPending) d.resolve(); // the older one lands late
    await slow;
    expect(searchResults.value[0]?.filename).toBe("crete.jpg");
  });

  it("clears results when the box is emptied", async () => {
    const p = runSearch("x");
    flush();
    await p;
    expect(searchResults.value).toHaveLength(1);

    await runSearch("   ");
    expect(searchResults.value).toHaveLength(0);
    expect(searching.value).toBe(false);
  });

  it("does not query on an empty box", async () => {
    await runSearch("");
    expect(calls.filter((c) => c.includes("q="))).toHaveLength(0);
  });

  it("clearSearch resets everything", async () => {
    const p = runSearch("x");
    flush();
    await p;
    clearSearch();
    expect(searchResults.value).toHaveLength(0);
    expect(searching.value).toBe(false);
  });
});
