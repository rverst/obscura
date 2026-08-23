// The stylesheet is not rendered anywhere in these tests, so this is the one
// thing about it worth asserting: that the two very different things called
// "tile" are never styled as if they were the same thing.
//
// They were, once. The timeline's `.tile { position: absolute }` reached the
// album tiles and stacked them all in one corner, and its opacity fade -- which
// only lifts when the timeline adds `loaded` -- left every album cover
// invisible. Both looked like data bugs and neither was.

// Read from disk rather than imported: vitest stubs CSS imports out, and
// `?raw` is stubbed with them, so an import would assert against "".
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = readFileSync(fileURLToPath(new URL("./styles.css", import.meta.url)), "utf8");

/** Every selector in the sheet, comments and declarations stripped. */
function selectors(): string[] {
  return css
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .split("}")
    .flatMap((block) => block.split("{")[0]?.split(",") ?? [])
    .map((s) => s.trim())
    .filter((s) => s.length > 0 && !s.startsWith("@"));
}

describe("tile styles stay scoped to the grid they belong to", () => {
  it("never styles a bare .tile", () => {
    const unscoped = selectors().filter((s) => /(^|\s)\.tile(\.|:|\s|$)/.test(s) && !/\.grid\s|\.tiles\s/.test(s));
    expect(unscoped).toEqual([]);
  });

  it("styles both kinds", () => {
    const all = selectors();
    expect(all.some((s) => s.startsWith(".grid .tile"))).toBe(true);
    expect(all.some((s) => s.startsWith(".tiles .tile"))).toBe(true);
  });
});
