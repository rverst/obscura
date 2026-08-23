import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { albums, albumError, reorderAlbum, type Album } from "./albums";

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

let moves: { id: number; after: number | null; before: number | null }[] = [];
let failNext = false;

beforeEach(() => {
  moves = [];
  failNext = false;
  albumError.value = "";
  albums.value = [album(1, "Alps"), album(2, "Bornholm"), album(3, "Croatia")];

  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const m = /\/api\/albums\/(\d+)\/position/.exec(path);
    if (m && init?.method === "PATCH") {
      const body = JSON.parse(String(init.body)) as {
        after: number | null;
        before: number | null;
      };
      moves.push({ id: Number(m[1]), after: body.after, before: body.before });
      if (failNext) {
        return new Response(JSON.stringify({ error: "nope" }), { status: 409 });
      }
      return new Response(null, { status: 204 });
    }
    // The reload that follows a successful move; it hands back what the
    // server now believes, which the test keeps equal to the local guess.
    if (path === "/api/albums") {
      return new Response(JSON.stringify({ albums: albums.value }), { status: 200 });
    }
    return new Response(JSON.stringify({}), { status: 200 });
  });
});

afterEach(() => vi.unstubAllGlobals());

const order = () => albums.value.map((a) => a.id);

describe("reorderAlbum", () => {
  it("rearranges the tiles before the server answers", async () => {
    const pending = reorderAlbum(3, null, 1);
    expect(order()).toEqual([3, 1, 2]);
    await pending;
    expect(moves).toEqual([{ id: 3, after: null, before: 1 }]);
  });

  it("places an album after the neighbour it was dropped past", async () => {
    await reorderAlbum(1, 3, null);
    expect(order()).toEqual([2, 3, 1]);
  });

  it("sends it to the end when neither neighbour is named", async () => {
    await reorderAlbum(1, null, null);
    expect(order()).toEqual([2, 3, 1]);
  });

  it("puts the old order back when the server refuses", async () => {
    failNext = true;
    await reorderAlbum(3, null, 1);
    expect(order()).toEqual([1, 2, 3]);
    // Silently keeping an order the server rejected would only survive until
    // the next reload, so the refusal is worth saying out loud.
    expect(albumError.value).toBe("nope");
  });

  it("ignores an album it does not know about", async () => {
    await reorderAlbum(99, null, 1);
    expect(order()).toEqual([1, 2, 3]);
    expect(moves).toEqual([]);
  });
});
