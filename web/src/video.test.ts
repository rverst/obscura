import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Media } from "./api";
import { albumItems } from "./albums";
import { posterError, setPoster } from "./poster";
import { items } from "./state";
import { searchResults } from "./trash";
import { lightbox, lightboxScope, loadVolume, saveVolume } from "./viewer";

function video(id: number, thumb = `t${id}`): Media {
  return {
    id,
    kind: "video",
    filename: `v${id}.mp4`,
    mime: "video/mp4",
    bytes: 100,
    sha256: "",
    width: 640,
    height: 480,
    created_at: 1000,
    duration_ms: 3000,
    has_thumb: true,
    v: { thumb, preview: `p-${thumb}`, original: `o${id}` },
  };
}

function memoryStorage(): Storage {
  const data = new Map<string, string>();
  return {
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
    clear: () => data.clear(),
    key: () => null,
    get length() {
      return data.size;
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("video volume", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("comes back as it was left", () => {
    saveVolume({ volume: 0.3, muted: true });
    expect(loadVolume()).toEqual({ volume: 0.3, muted: true });
  });

  it("leaves the browser default alone until something was saved", () => {
    expect(loadVolume()).toBeNull();
  });

  // A volume outside 0..1 makes the media element throw on assignment.
  it("ignores a stored value the player would reject", () => {
    localStorage.setItem("obscura.video.volume", JSON.stringify({ volume: 7, muted: false }));
    expect(loadVolume()).toBeNull();
    localStorage.setItem("obscura.video.volume", "not json");
    expect(loadVolume()).toBeNull();
  });

  it("works, forgetfully, where storage is refused", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
    });
    expect(() => saveVolume({ volume: 0.5, muted: false })).not.toThrow();
    expect(loadVolume()).toBeNull();
  });
});

describe("choosing a video's thumbnail", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockReset();
    items.value = [];
    albumItems.value = [];
    searchResults.value = [];
    lightbox.value = null;
    lightboxScope.value = [];
    posterError.value = null;
  });

  it("sends the position in whole milliseconds", async () => {
    fetchMock.mockResolvedValue({ ok: true, status: 200, json: async () => video(1, "new") });
    await setPoster(1, 1234.56);
    const poster = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/poster"));
    expect(poster?.[1]?.method).toBe("PUT");
    expect(JSON.parse(poster?.[1]?.body as string)).toEqual({ at_ms: 1235 });
  });

  // A tile keeps its cached still until its URL changes, and the URL carries
  // the token. Missing a list means that view shows the old frame.
  it("gives every list showing the clip the new still", async () => {
    fetchMock.mockImplementation(async (url: string) =>
      String(url).endsWith("/poster")
        ? { ok: true, status: 200, json: async () => video(1, "new") }
        : { ok: true, status: 200, json: async () => ({ albums: [] }) },
    );
    items.value = [video(1), video(2)];
    albumItems.value = [{ ...video(1), sort_key: "a0" }];
    searchResults.value = [video(1)];
    lightboxScope.value = [video(1)];
    lightbox.value = video(1);

    await setPoster(1, 500);

    expect(items.value[0]?.v?.thumb).toBe("new");
    expect(items.value[1]?.v?.thumb).toBe("t2");
    expect(albumItems.value[0]?.v?.thumb).toBe("new");
    expect(albumItems.value[0]?.sort_key).toBe("a0");
    expect(searchResults.value[0]?.v?.thumb).toBe("new");
    expect(lightboxScope.value[0]?.v?.thumb).toBe("new");
    expect(lightbox.value?.v?.preview).toBe("p-new");
  });

  it("reports a refusal against the clip it was for", async () => {
    fetchMock.mockResolvedValue({
      ok: false,
      status: 400,
      json: async () => ({ error: "no frame at that position" }),
    });
    items.value = [video(1)];

    await setPoster(1, 99_000);

    expect(posterError.value).toEqual({ id: 1, message: "no frame at that position" });
    expect(items.value[0]?.v?.thumb).toBe("t1");
  });
});
