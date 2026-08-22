import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Media, TrashedMedia } from "./api";
import { albumItems } from "./albums";
import { setFavorite } from "./favorites";
import { items } from "./state";
import { searchResults, trashMedia } from "./trash";
import { isTrashed, lightbox, lightboxScope, openLightbox } from "./viewer";

function media(id: number, extra: Partial<Media> = {}): Media {
  return {
    id,
    kind: "photo",
    filename: `p${id}.jpg`,
    mime: "image/jpeg",
    bytes: 1000,
    sha256: "aa",
    width: 100,
    height: 100,
    created_at: 1700000000,
    has_thumb: true,
    ...extra,
  };
}

const fetchMock = vi.fn();

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ items: [], albums: [] }),
  });
  items.value = [];
  albumItems.value = [];
  searchResults.value = [];
  lightbox.value = null;
  lightboxScope.value = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("deleting a photo", () => {
  // The reported bug: deleting from inside an album left the thumbnail on
  // screen until the page was reloaded, because only the timeline was updated.
  it("removes it from every list it is currently in", async () => {
    items.value = [media(1), media(2)];
    albumItems.value = [{ ...media(1), sort_key: "a0" }, { ...media(3), sort_key: "a1" }];
    searchResults.value = [media(1), media(4)];

    await trashMedia(1);

    expect(items.value.map((m) => m.id)).toEqual([2]);
    expect(albumItems.value.map((m) => m.id)).toEqual([3]);
    expect(searchResults.value.map((m) => m.id)).toEqual([4]);
  });

  // Album tiles show a count. Leaving it stale is the same bug wearing a
  // different hat, so the album list is refetched.
  it("asks for the album counts again", async () => {
    items.value = [media(1)];
    await trashMedia(1);

    const paths = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(paths.some((p) => p.startsWith("/api/albums"))).toBe(true);
  });

  it("puts everything back when the server refuses", async () => {
    items.value = [media(1), media(2)];
    albumItems.value = [{ ...media(1), sort_key: "a0" }];
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => ({ error: "nope" }),
    });

    await trashMedia(1);

    expect(items.value.map((m) => m.id)).toEqual([1, 2]);
    expect(albumItems.value.map((m) => m.id)).toEqual([1]);
  });

  it("closes the lightbox if it was showing that photo", async () => {
    const list = [media(1), media(2)];
    items.value = list;
    openLightbox(list[0]!, list);

    await trashMedia(1);

    expect(lightbox.value).toBeNull();
  });
});

describe("trashed records", () => {
  // The lightbox has to tell the two apart to offer Restore rather than a
  // Delete button for something already deleted.
  it("are recognised by the deadline they carry", () => {
    const trashed: TrashedMedia = {
      ...media(1),
      deleted_at: 1700000000,
      purge_at: 1702592000,
    };
    expect(isTrashed(trashed)).toBe(true);
    expect(isTrashed(media(2))).toBe(false);
  });
});

describe("favourites", () => {
  it("mark the photo everywhere it is on screen at once", async () => {
    const list = [media(1), media(2)];
    items.value = list;
    albumItems.value = [{ ...media(1), sort_key: "a0" }];
    openLightbox(list[0]!, list);

    await setFavorite(1, true);

    expect(items.value[0]!.favorite).toBe(true);
    expect(albumItems.value[0]!.favorite).toBe(true);
    expect(lightbox.value?.favorite).toBe(true);
    expect(lightboxScope.value[0]!.favorite).toBe(true);
    // And leave the others alone.
    expect(items.value[1]!.favorite).toBeFalsy();
  });

  it("roll back when the server refuses", async () => {
    items.value = [media(1)];
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => ({ error: "nope" }),
    });

    await setFavorite(1, true);

    expect(items.value[0]!.favorite).toBeFalsy();
  });

  it("use PUT to set and DELETE to clear, never a toggle", async () => {
    items.value = [media(1)];
    await setFavorite(1, true);
    expect(fetchMock.mock.calls[0]![1].method).toBe("PUT");

    fetchMock.mockClear();
    await setFavorite(1, false);
    expect(fetchMock.mock.calls[0]![1].method).toBe("DELETE");
  });
});
