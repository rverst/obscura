import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { albumItems, currentAlbum, moveItem, type AlbumMedia } from "./albums";

function item(id: number, sortKey: string): AlbumMedia {
  return {
    id,
    kind: "photo",
    filename: `p${id}.jpg`,
    mime: "image/jpeg",
    bytes: 100,
    sha256: "",
    width: 100,
    height: 100,
    created_at: 0,
    has_thumb: true,
    sort_key: sortKey,
  };
}

let moves: { mediaID: number; after: number | null; before: number | null }[] = [];
let failNext = false;

beforeEach(() => {
  moves = [];
  failNext = false;
  currentAlbum.value = {
    id: 1,
    title: "Trip",
    description: "",
    item_count: 4,
    created_at: 0,
  };
  albumItems.value = [item(10, "a0"), item(20, "a1"), item(30, "a2"), item(40, "a3")];

  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const m = /\/api\/albums\/1\/items\/(\d+)\/position/.exec(path);
    if (m && init?.method === "PATCH") {
      const body = JSON.parse(String(init.body)) as { after: number | null; before: number | null };
      moves.push({ mediaID: Number(m[1]), after: body.after, before: body.before });
      if (failNext) {
        return new Response(JSON.stringify({ error: "nope" }), { status: 409 });
      }
      return new Response(JSON.stringify({ sort_key: "aZ" }), { status: 200 });
    }
    return new Response(JSON.stringify({}), { status: 200 });
  });
});

afterEach(() => vi.unstubAllGlobals());

const order = () => albumItems.value.map((m) => m.id);

describe("moveItem", () => {
  it("reorders locally before the server answers", async () => {
    const pending = moveItem(40, 0);
    // The list must already reflect the drag, or it feels broken while the
    // request is in flight.
    expect(order()).toEqual([40, 10, 20, 30]);
    await pending;
    expect(order()).toEqual([40, 10, 20, 30]);
  });

  // The server is told which neighbours the photo landed between, not an index.
  it("sends the resulting neighbours", async () => {
    await moveItem(40, 1);
    expect(order()).toEqual([10, 40, 20, 30]);
    expect(moves).toEqual([{ mediaID: 40, after: 10, before: 20 }]);
  });

  it("sends null for the start of the album", async () => {
    await moveItem(30, 0);
    expect(moves[0]).toEqual({ mediaID: 30, after: null, before: 10 });
  });

  it("sends null for the end of the album", async () => {
    await moveItem(10, 3);
    expect(moves[0]).toEqual({ mediaID: 10, after: 40, before: null });
  });

  it("adopts the sort key the server assigned", async () => {
    await moveItem(40, 0);
    expect(albumItems.value.find((m) => m.id === 40)?.sort_key).toBe("aZ");
  });

  // Keeping a local order the server rejected would be worse than a visible
  // snap back: the next reload would undo it anyway, silently.
  it("restores the previous order when the server refuses", async () => {
    failNext = true;
    await moveItem(40, 0);
    expect(order()).toEqual([10, 20, 30, 40]);
  });

  it("does nothing when the item did not move", async () => {
    await moveItem(20, 1);
    expect(moves).toHaveLength(0);
    expect(order()).toEqual([10, 20, 30, 40]);
  });

  it("ignores an unknown item", async () => {
    await moveItem(999, 0);
    expect(moves).toHaveLength(0);
    expect(order()).toEqual([10, 20, 30, 40]);
  });
});
