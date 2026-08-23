import { describe, expect, it } from "vitest";
import { format, parse, withoutPhoto, type Route } from "./router";

// parse and format are the whole contract of a URL, and they are pure -- no
// history, no location, no DOM. Everything else in the router is a thin shell
// over these two.

const cases: { url: string; route: Route }[] = [
  { url: "/", route: { view: "album", albumID: null, photo: null } },
  { url: "/a/3", route: { view: "album", albumID: 3, photo: null } },
  { url: "/a/3/p/42", route: { view: "album", albumID: 3, photo: 42 } },
  { url: "/all", route: { view: "all", photo: null } },
  { url: "/all/p/42", route: { view: "all", photo: 42 } },
  { url: "/trash", route: { view: "trash", photo: null } },
  { url: "/trash/p/42", route: { view: "trash", photo: 42 } },
  { url: "/search?q=croatia", route: { view: "search", query: "croatia", photo: null } },
  { url: "/search/p/42?q=croatia", route: { view: "search", query: "croatia", photo: 42 } },
];

function split(url: string): [string, string] {
  const at = url.indexOf("?");
  return at < 0 ? [url, ""] : [url.slice(0, at), url.slice(at)];
}

describe("reading and writing URLs", () => {
  for (const { url, route } of cases) {
    it(`reads ${url}`, () => {
      const [pathname, search] = split(url);
      expect(parse(pathname, search)).toEqual(route);
    });

    it(`writes ${url}`, () => {
      expect(format(route)).toBe(url);
    });
  }

  it("survives a round trip through a query that needs escaping", () => {
    const route: Route = { view: "search", query: "a b&c=d/e", photo: null };
    const [pathname, search] = split(format(route));
    expect(parse(pathname, search)).toEqual(route);
  });

  it("ignores a trailing slash", () => {
    expect(parse("/a/3/", "")).toEqual({ view: "album", albumID: 3, photo: null });
  });
});

describe("URLs that do not name anything", () => {
  const root: Route = { view: "album", albumID: null, photo: null };

  // A URL is user input. A typo should land somewhere sensible rather than on
  // a broken screen, so everything unrecognised is the library root.
  it.each([
    ["/nonsense", ""],
    ["/a/notanumber", ""],
    ["/a", ""],
    ["/p/42", ""],
    // Search with nothing to search for: typing into the box cannot produce
    // this state either, since clearing the box leaves search entirely.
    ["/search", ""],
    ["/search", "?q="],
    ["/search", "?q=%20%20"],
  ])("falls back to the root for %s%s", (pathname, search) => {
    expect(parse(pathname, search)).toEqual(root);
  });
});

describe("withoutPhoto", () => {
  it("keeps where you are and drops what was open on top of it", () => {
    expect(withoutPhoto({ view: "album", albumID: 3, photo: 42 })).toEqual({
      view: "album",
      albumID: 3,
      photo: null,
    });
    expect(withoutPhoto({ view: "search", query: "x", photo: 7 })).toEqual({
      view: "search",
      query: "x",
      photo: null,
    });
  });
});
