import { describe, expect, it } from "vitest";
import { layout, visibleRange, type LayoutOptions } from "./layout";
import type { Media } from "./api";

const opts: LayoutOptions = {
  containerWidth: 1000,
  targetRowHeight: 200,
  gap: 4,
  maxRowHeight: 400,
};

let nextID = 1;
function photo(width: number, height: number): Media {
  return {
    id: nextID++,
    kind: "photo",
    filename: "p.jpg",
    mime: "image/jpeg",
    bytes: 1000,
    sha256: "",
    width,
    height,
    created_at: 0,
    has_thumb: true,
  };
}

/** Width consumed by a row, including the gaps between its items. */
function rowWidth(items: { width: number }[], gap: number): number {
  return items.reduce((sum, it) => sum + it.width, 0) + gap * (items.length - 1);
}

describe("layout", () => {
  it("fills the container width exactly on complete rows", () => {
    const media = Array.from({ length: 30 }, () => photo(3000, 2000));
    const { rows } = layout(media, opts);
    expect(rows.length).toBeGreaterThan(1);

    // Every row but the last must fill the width; that is what makes the grid
    // read as a grid rather than a ragged pile.
    for (const row of rows.slice(0, -1)) {
      expect(rowWidth(row.items, opts.gap)).toBeCloseTo(opts.containerWidth, 5);
    }
  });

  it("preserves each photo's aspect ratio", () => {
    const media = [photo(3000, 2000), photo(1000, 2000), photo(4000, 1000)];
    const { rows } = layout(media, opts);
    for (const row of rows) {
      for (const item of row.items) {
        const want = item.media.width / item.media.height;
        expect(item.width / item.height).toBeCloseTo(want, 5);
      }
    }
  });

  // A single leftover photo stretched to full width is the classic justified
  // layout bug: one holiday snap suddenly two thousand pixels wide.
  it("does not stretch a short final row", () => {
    const media = [...Array.from({ length: 8 }, () => photo(3000, 2000)), photo(3000, 2000)];
    const { rows } = layout(media, opts);
    const last = rows[rows.length - 1]!;
    expect(last.height).toBeLessThanOrEqual(opts.targetRowHeight + 0.001);
  });

  it("caps row height so a panorama does not become a wall", () => {
    // One very wide image alone in a row would otherwise be scaled up hugely.
    const { rows } = layout([photo(10000, 1000)], { ...opts, maxRowHeight: 300 });
    expect(rows[0]!.height).toBeLessThanOrEqual(300);
  });

  it("stacks rows without overlap and reports the total height", () => {
    const media = Array.from({ length: 25 }, () => photo(3000, 2000));
    const { rows, totalHeight } = layout(media, opts);

    for (let i = 1; i < rows.length; i++) {
      const prev = rows[i - 1]!;
      expect(rows[i]!.y).toBeCloseTo(prev.y + prev.height + opts.gap, 5);
    }
    const last = rows[rows.length - 1]!;
    // The trailing gap belongs between rows, not after the final one.
    expect(totalHeight).toBeCloseTo(last.y + last.height, 5);
  });

  it("places every photo exactly once", () => {
    const media = Array.from({ length: 37 }, (_, i) => photo(2000 + i * 100, 1500));
    const { rows } = layout(media, opts);
    const ids = rows.flatMap((r) => r.items.map((it) => it.media.id));
    expect(ids).toHaveLength(media.length);
    expect(new Set(ids).size).toBe(media.length);
  });

  it("positions items left to right with the configured gap", () => {
    const { rows } = layout(Array.from({ length: 10 }, () => photo(3000, 2000)), opts);
    for (const row of rows) {
      expect(row.items[0]!.x).toBe(0);
      for (let i = 1; i < row.items.length; i++) {
        const prev = row.items[i - 1]!;
        expect(row.items[i]!.x).toBeCloseTo(prev.x + prev.width + opts.gap, 5);
      }
    }
  });

  // Zero dimensions come from media the server could not measure. Dividing by
  // them would produce NaN and take the entire grid down.
  it("survives unknown dimensions", () => {
    const broken = { ...photo(0, 0), width: 0, height: 0 };
    const { rows, totalHeight } = layout([broken, photo(3000, 2000)], opts);
    expect(Number.isFinite(totalHeight)).toBe(true);
    for (const row of rows) {
      for (const item of row.items) {
        expect(Number.isFinite(item.width)).toBe(true);
        expect(Number.isFinite(item.height)).toBe(true);
        expect(item.width).toBeGreaterThan(0);
      }
    }
  });

  it("handles an empty library and a zero-width container", () => {
    expect(layout([], opts)).toEqual({ rows: [], totalHeight: 0 });
    expect(layout([photo(3000, 2000)], { ...opts, containerWidth: 0 })).toEqual({
      rows: [],
      totalHeight: 0,
    });
  });
});

describe("visibleRange", () => {
  const media = Array.from({ length: 200 }, () => photo(3000, 2000));
  const { rows } = layout(media, opts);

  it("returns only rows intersecting the viewport", () => {
    const { start, end } = visibleRange(rows, 1000, 800, 0);
    expect(start).toBeLessThan(end);
    for (let i = start; i < end; i++) {
      const row = rows[i]!;
      expect(row.y + row.height).toBeGreaterThanOrEqual(1000);
      expect(row.y).toBeLessThanOrEqual(1800);
    }
    // Rows just outside must be excluded, otherwise virtualisation buys nothing.
    if (start > 0) {
      const above = rows[start - 1]!;
      expect(above.y + above.height).toBeLessThan(1000);
    }
  });

  it("renders a small fraction of a large library", () => {
    const { start, end } = visibleRange(rows, 5000, 900, 200);
    expect(end - start).toBeLessThan(rows.length / 4);
  });

  it("includes overscan rows on both sides", () => {
    const tight = visibleRange(rows, 3000, 600, 0);
    const loose = visibleRange(rows, 3000, 600, 600);
    expect(loose.start).toBeLessThanOrEqual(tight.start);
    expect(loose.end).toBeGreaterThanOrEqual(tight.end);
  });

  it("clamps at the top and bottom", () => {
    const top = visibleRange(rows, 0, 600, 0);
    expect(top.start).toBe(0);

    const last = rows[rows.length - 1]!;
    const bottom = visibleRange(rows, last.y, 600, 0);
    expect(bottom.end).toBe(rows.length);
  });

  it("returns an empty range for an empty layout", () => {
    expect(visibleRange([], 0, 600, 0)).toEqual({ start: 0, end: 0 });
  });

  it("returns nothing when scrolled past the end", () => {
    const last = rows[rows.length - 1]!;
    const { start, end } = visibleRange(rows, last.y + last.height + 10_000, 600, 0);
    expect(end - start).toBe(0);
  });
});
