import { beforeEach, describe, expect, it } from "vitest";
import type { Media } from "./api";
import { insertMedia, items } from "./state";

function media(
  id: number,
  takenAt: number | undefined,
  createdAt = 5000,
): Media {
  return {
    id,
    kind: "photo",
    filename: `p${id}.jpg`,
    mime: "image/jpeg",
    bytes: 100,
    sha256: "",
    width: 100,
    height: 100,
    created_at: createdAt,
    taken_at: takenAt,
    has_thumb: true,
  };
}

beforeEach(() => {
  items.value = [];
});

describe("insertMedia", () => {
  it("places a newly uploaded item by capture time, not at the top", () => {
    // Timeline is newest first.
    items.value = [media(1, 3000), media(2, 2000), media(3, 1000)];
    insertMedia(media(4, 2500));

    expect(items.value.map((m) => m.id)).toEqual([1, 4, 2, 3]);
  });

  it("puts a genuinely newest item first", () => {
    items.value = [media(1, 3000), media(2, 2000)];
    insertMedia(media(4, 9000));
    expect(items.value.map((m) => m.id)).toEqual([4, 1, 2]);
  });

  it("falls back to upload time when a photo has no capture time", () => {
    items.value = [media(1, 3000), media(2, 1000)];
    // No taken_at, so created_at decides.
    insertMedia(media(4, undefined, 2000));
    expect(items.value.map((m) => m.id)).toEqual([1, 4, 2]);
  });

  it("ignores an item that belongs on a page not loaded yet", () => {
    // More pages exist below, and this photo sorts older than everything held.
    items.value = [media(1, 3000), media(2, 2000)];
    insertMedia(media(4, 500));
    // Showing it here would put it in the wrong place; it arrives on scroll.
    expect(items.value.map((m) => m.id)).toEqual([1, 2]);
  });

  it("does not insert the same item twice", () => {
    items.value = [media(1, 3000)];
    insertMedia(media(1, 3000));
    expect(items.value).toHaveLength(1);
  });

  it("handles an empty library", () => {
    insertMedia(media(1, 3000));
    expect(items.value.map((m) => m.id)).toEqual([1]);
  });
});
