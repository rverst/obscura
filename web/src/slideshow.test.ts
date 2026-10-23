import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Media } from "./api";
import { albumItems, currentAlbum } from "./albums";
import { stepLightbox } from "./router";
import { items, reloadTimeline, timelineTotal } from "./state";
import { lightbox, lightboxScope, openLightbox } from "./viewer";

// The lightbox runs round: past the last photo comes the first. In the
// timeline "the last photo" is only the last one loaded, so it has to fetch
// the next page and step into that before it may wrap.

function media(id: number): Media {
  return {
    id,
    kind: "photo",
    filename: `p${id}.jpg`,
    mime: "image/jpeg",
    bytes: 1,
    sha256: "aa",
    width: 10,
    height: 10,
    created_at: 1000 - id,
    has_thumb: true,
    favorite: false,
  };
}

/** Enough of location and history for the router to walk photos. */
function fakeBrowser(url: string) {
  const loc = { pathname: "", search: "" };
  const go = (to: string) => {
    const at = to.indexOf("?");
    loc.pathname = at < 0 ? to : to.slice(0, at);
    loc.search = at < 0 ? "" : to.slice(at);
  };
  go(url);
  vi.stubGlobal("location", loc);
  vi.stubGlobal("history", {
    state: null,
    replaceState: (_s: unknown, _t: string, to: string) => go(to),
    pushState: (_s: unknown, _t: string, to: string) => go(to),
  });
  return loc;
}

const fetchMock = vi.fn();
const page = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

/** Lets the route the step navigated to finish applying. */
const settle = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
});

afterEach(() => {
  vi.unstubAllGlobals();
  lightbox.value = null;
  lightboxScope.value = [];
});

describe("stepping through an album", () => {
  beforeEach(() => {
    albumItems.value = [1, 2, 3].map((id) => ({ ...media(id), sort_key: String(id) }));
    currentAlbum.value = {
      id: 7,
      title: "Crete",
      description: "",
      item_count: 3,
      total_count: 3,
      created_at: 0,
    };
  });

  it("wraps from the last photo to the first", async () => {
    const loc = fakeBrowser("/a/7/p/3");
    openLightbox(albumItems.value[2]!, albumItems.value);
    await stepLightbox(1);
    await settle();
    expect(loc.pathname).toBe("/a/7/p/1");
    expect(lightbox.value?.id).toBe(1);
  });

  it("wraps from the first photo back to the last", async () => {
    const loc = fakeBrowser("/a/7/p/1");
    openLightbox(albumItems.value[0]!, albumItems.value);
    await stepLightbox(-1);
    await settle();
    expect(loc.pathname).toBe("/a/7/p/3");
  });
});

describe("stepping through the paged timeline", () => {
  it("fetches the next page before wrapping, and only then wraps", async () => {
    fetchMock.mockResolvedValueOnce(
      page({ items: [media(1), media(2)], next: { before: 1, before_id: 2 }, total: 3 }),
    );
    await reloadTimeline();
    expect(timelineTotal.value).toBe(3);

    const loc = fakeBrowser("/all/p/2");
    openLightbox(items.value[1]!, items.value);

    fetchMock.mockResolvedValueOnce(page({ items: [media(3)], total: 3 }));
    await stepLightbox(1);
    await settle();
    expect(loc.pathname).toBe("/all/p/3");
    expect(items.value.map((m) => m.id)).toEqual([1, 2, 3]);

    // Everything is loaded now, so the next step goes round.
    await stepLightbox(1);
    await settle();
    expect(loc.pathname).toBe("/all/p/1");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("stays put when the next page fails to arrive", async () => {
    fetchMock.mockResolvedValueOnce(
      page({ items: [media(1), media(2)], next: { before: 1, before_id: 2 }, total: 3 }),
    );
    await reloadTimeline();
    const loc = fakeBrowser("/all/p/2");
    openLightbox(items.value[1]!, items.value);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500, json: async () => ({ error: "boom" }) });
    await stepLightbox(1);
    await settle();
    expect(loc.pathname).toBe("/all/p/2");
  });
});

describe("the timeline's filters", () => {
  it("reach the server as query parameters, so paging keeps working", async () => {
    const { api } = await import("./api");
    fetchMock.mockResolvedValue(page({ items: [] }));
    await api.listMedia(undefined, 100, { favorite: true, unfiled: true });
    const url = String(fetchMock.mock.calls[0]![0]);
    expect(url).toContain("favorite=1");
    expect(url).toContain("unfiled=1");

    await api.listMedia();
    expect(String(fetchMock.mock.calls[1]![0])).not.toMatch(/favorite|unfiled/);
  });
});
