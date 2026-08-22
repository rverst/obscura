import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Media } from "./api";
import {
  albumItems,
  albumPath,
  albums,
  childAlbums,
  currentAlbum,
  deleteAlbum,
  moveAlbum,
  openLibraryRoot,
  setAlbumCover,
  type Album,
} from "./albums";
import { favoritesOnly, filterFavorites } from "./favorites";

function album(id: number, title: string, parent?: number): Album {
  return {
    id,
    title,
    description: "",
    parent_id: parent,
    item_count: 0,
    total_count: 0,
    created_at: 0,
  };
}

function media(id: number, favorite = false): Media {
  return {
    id,
    kind: "photo",
    filename: `p${id}.jpg`,
    mime: "image/jpeg",
    bytes: 1,
    sha256: "aa",
    width: 10,
    height: 10,
    created_at: 0,
    has_thumb: true,
    favorite,
  };
}

const fetchMock = vi.fn();

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  fetchMock.mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ albums: [] }),
  });
  albums.value = [];
  currentAlbum.value = null;
  albumPath.value = [];
  albumItems.value = [];
  favoritesOnly.value = false;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the album tree", () => {
  beforeEach(() => {
    albums.value = [
      album(1, "Croatia"),
      album(2, "2024", 1),
      album(3, "2025", 1),
      album(4, "Garden"),
      album(5, "July", 2),
    ];
  });

  it("shows only top-level albums at the root", () => {
    expect(childAlbums(null).map((a) => a.title)).toEqual(["Croatia", "Garden"]);
  });

  it("shows an album's own children when inside it", () => {
    expect(childAlbums(1).map((a) => a.title)).toEqual(["2024", "2025"]);
    expect(childAlbums(2).map((a) => a.title)).toEqual(["July"]);
    expect(childAlbums(3)).toEqual([]);
  });

  // The whole tree arrives in one response, so navigating a level is not a
  // request. This is the property that makes that safe.
  it("needs no request to walk a level", () => {
    childAlbums(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("moving an album", () => {
  it("asks the server to re-parent it", async () => {
    await moveAlbum(2, 1);
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(String(url)).toBe("/api/albums/2/parent");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body)).toEqual({ parent_id: 1 });
  });

  it("uses null to lift one to the top level", async () => {
    await moveAlbum(2, null);
    expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ parent_id: null });
  });
});

describe("deleting an album", () => {
  // Deleting takes the sub-albums with it, so an open album that sat anywhere
  // under the deleted one no longer exists. Staying there would show a
  // breadcrumb into nothing.
  it("leaves for the root when the open album was underneath it", async () => {
    currentAlbum.value = album(5, "July", 2);
    albumPath.value = [
      { id: 1, title: "Croatia" },
      { id: 2, title: "2024" },
      { id: 5, title: "July" },
    ];

    await deleteAlbum(1);

    expect(currentAlbum.value).toBeNull();
    expect(albumPath.value).toEqual([]);
  });

  it("stays put when an unrelated album is deleted", async () => {
    currentAlbum.value = album(4, "Garden");
    albumPath.value = [{ id: 4, title: "Garden" }];

    await deleteAlbum(1);

    expect(currentAlbum.value?.id).toBe(4);
  });
});

describe("leaving for the library root", () => {
  it("clears the open album and its trail", () => {
    currentAlbum.value = album(1, "Croatia");
    albumPath.value = [{ id: 1, title: "Croatia" }];
    albumItems.value = [{ ...media(1), sort_key: "a0" }];

    openLibraryRoot();

    expect(currentAlbum.value).toBeNull();
    expect(albumPath.value).toEqual([]);
    expect(albumItems.value).toEqual([]);
  });
});

describe("the favourites filter", () => {
  it("passes everything through when off", () => {
    const list = [media(1, true), media(2)];
    expect(filterFavorites(list).map((m) => m.id)).toEqual([1, 2]);
  });

  it("narrows a loaded list when on", () => {
    favoritesOnly.value = true;
    const list = [media(1, true), media(2), media(3, true)];
    expect(filterFavorites(list).map((m) => m.id)).toEqual([1, 3]);
  });
});

describe("the album cover", () => {
  beforeEach(() => {
    currentAlbum.value = album(1, "Croatia");
  });

  it("can be pinned to one photo", async () => {
    await setAlbumCover(7);

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(String(url)).toBe("/api/albums/1/cover");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body)).toEqual({ media_id: 7 });
    expect(currentAlbum.value?.cover_media_id).toBe(7);
  });

  // Overriding the automatic choice has to be reversible, or the first
  // click is permanent.
  it("goes back to automatic with null", async () => {
    currentAlbum.value = { ...album(1, "Croatia"), cover_media_id: 7 };

    await setAlbumCover(null);

    expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ media_id: null });
    expect(currentAlbum.value?.cover_media_id).toBeUndefined();
  });

  it("refreshes the tiles, which are what show the cover", async () => {
    await setAlbumCover(7);

    const paths = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(paths).toContain("/api/albums");
  });
});
