// A session that lapsed while the window sat open.
//
// The reported symptom: coming back to an open tab and clicking an album gave
// "not logged in" as an error message beside a library that was no longer
// there, while the timeline kept showing thumbnails out of the service worker's
// cache. Only one request in the whole app had ever recognised a 401 -- the
// timeline's -- so every other click reported it as an ordinary failure.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { albumApi, albums, loadAlbums, albumError } from "./albums";
import { api } from "./api";
import { authError, items, login, screen, username } from "./state";
import { setFavorite } from "./favorites";
import { loadTrash, trashed, trashError } from "./trash";

function unauthorized() {
  return new Response(JSON.stringify({ error: "not logged in" }), { status: 401 });
}

beforeEach(() => {
  screen.value = "gallery";
  username.value = "demo";
  authError.value = "";
  albumError.value = "";
  albums.value = [];
  items.value = [];
  trashed.value = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    // The status check the login screen makes on the way out.
    if (String(input) === "/api/status") {
      return new Response(JSON.stringify({ initialized: true, authenticated: false, passkeys: false }), {
        status: 200,
      });
    }
    return unauthorized();
  });
});

afterEach(() => vi.unstubAllGlobals());

describe("a request that comes back unauthorised", () => {
  it("leaves the gallery when an album is opened", async () => {
    await loadAlbums();
    expect(screen.value).toBe("login");
  });

  // Every one of these was a way to sit in a dead session before.
  it.each([
    ["listing albums", () => loadAlbums()],
    ["reading one album", () => albumApi.get(1).catch(() => undefined)],
    ["the trash", () => loadTrash()],
    ["favouriting", () => setFavorite(1, true)],
    ["the timeline", () => api.listMedia().catch(() => undefined)],
  ])("leaves the gallery from %s", async (_name, act) => {
    await act();
    expect(screen.value).toBe("login");
  });

  it("is a proper logout, not just a change of screen", async () => {
    albums.value = [
      { id: 1, title: "Alps", description: "", item_count: 0, total_count: 0, created_at: 0 },
    ];
    items.value = [];

    await loadAlbums();

    expect(username.value).toBe("");
    expect(albums.value).toEqual([]);
    // Being thrown back to a login form with no reason given is a mystery.
    expect(authError.value).toMatch(/expired/i);
  });

  // The message must not outlive the session it belongs to: signing back in
  // would otherwise show "not logged in" in red under a working album grid.
  it("leaves no error behind for the next session", async () => {
    await loadAlbums();
    await loadTrash();
    expect(albumError.value).toBe("");
    expect(trashError.value).toBe("");
  });

  // Signing in with the wrong password answers 401 too. Treating that as a
  // lapsed session would wipe the message the login form just put up.
  it("means nothing outside the gallery", async () => {
    screen.value = "login";
    authError.value = "";

    await api.login("demo", "wrong").catch(() => undefined);

    expect(screen.value).toBe("login");
    expect(authError.value).toBe("");
  });

  // The login form still has to be able to say why it refused.
  it("still reports a wrong password on the login screen", async () => {
    screen.value = "login";
    await login("demo", "wrong");
    expect(authError.value).toBe("not logged in");
    expect(screen.value).toBe("login");
  });
});
