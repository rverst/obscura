// Justified grid layout.
//
// Photos keep their aspect ratio and are packed into rows that each fill the
// container exactly, the way Google Photos and Flickr lay out a timeline. A
// fixed square grid would be far simpler, but it crops every photo to a square,
// and a gallery whose whole point is looking at photographs should not do that.
//
// The algorithm is greedy: keep adding photos to a row until they are, in sum,
// wider than the container at the target height, then scale the row so it fits
// exactly. Greedy is not optimal -- a dynamic-programming pass would produce
// slightly more even rows -- but it is O(n), runs on every resize, and the
// difference is not visible.

import type { Media } from "./api";

export interface LaidOutItem {
  media: Media;
  width: number;
  height: number;
  /** Offset from the left edge of the row. */
  x: number;
}

export interface Row {
  items: LaidOutItem[];
  height: number;
  /** Offset from the top of the whole grid. */
  y: number;
}

export interface Layout {
  rows: Row[];
  totalHeight: number;
}

export interface LayoutOptions {
  containerWidth: number;
  targetRowHeight: number;
  gap: number;
  /** Rows may not exceed this, or a single panorama becomes a wall of image. */
  maxRowHeight: number;
}

/**
 * aspectOf guards against the degenerate cases. Media whose dimensions are
 * unknown (0) would otherwise divide by zero and take the whole layout with
 * them; a square is a harmless assumption until the image itself loads.
 */
function aspectOf(m: Media): number {
  if (m.width > 0 && m.height > 0) return m.width / m.height;
  return 1;
}

export function layout(media: Media[], opt: LayoutOptions): Layout {
  const rows: Row[] = [];
  if (opt.containerWidth <= 0 || media.length === 0) {
    return { rows, totalHeight: 0 };
  }

  let current: Media[] = [];
  let aspectSum = 0;
  let y = 0;

  const flush = (isLast: boolean) => {
    if (current.length === 0) return;

    const totalGap = opt.gap * (current.length - 1);
    const available = opt.containerWidth - totalGap;
    // Height that makes this row fill the width exactly.
    let height = available / aspectSum;

    // The final row is special: it usually has too few photos to fill the
    // width, and stretching them to do so would blow a single leftover image up
    // to full width. Keep it at the target height instead and let it end short.
    if (isLast && height > opt.targetRowHeight) {
      height = opt.targetRowHeight;
    }
    height = Math.min(height, opt.maxRowHeight);

    const laid: LaidOutItem[] = [];
    let x = 0;
    for (const m of current) {
      const width = height * aspectOf(m);
      laid.push({ media: m, width, height, x });
      x += width + opt.gap;
    }
    rows.push({ items: laid, height, y });
    y += height + opt.gap;

    current = [];
    aspectSum = 0;
  };

  for (const m of media) {
    current.push(m);
    aspectSum += aspectOf(m);

    // Would this row, at the target height, already overflow the container?
    const totalGap = opt.gap * (current.length - 1);
    if (aspectSum * opt.targetRowHeight + totalGap >= opt.containerWidth) {
      flush(false);
    }
  }
  flush(true);

  // The trailing gap belongs between rows, not after the last one.
  const totalHeight = rows.length > 0 ? y - opt.gap : 0;
  return { rows, totalHeight };
}

/**
 * visibleRange finds the rows intersecting the viewport, by binary search over
 * row offsets.
 *
 * This is the whole of the virtualisation: because the layout computes an exact
 * height for every row up front, there is no need to measure anything in the
 * DOM, and therefore no need for a virtualisation library. A 10k-photo library
 * renders as a few dozen elements.
 */
export function visibleRange(
  rows: Row[],
  scrollTop: number,
  viewportHeight: number,
  overscan: number,
): { start: number; end: number } {
  if (rows.length === 0) return { start: 0, end: 0 };

  const top = scrollTop - overscan;
  const bottom = scrollTop + viewportHeight + overscan;

  // First row whose bottom edge is below the top of the viewport.
  let lo = 0;
  let hi = rows.length - 1;
  let start = rows.length;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    const row = rows[mid]!;
    if (row.y + row.height >= top) {
      start = mid;
      hi = mid - 1;
    } else {
      lo = mid + 1;
    }
  }
  if (start === rows.length) return { start: rows.length, end: rows.length };

  // Walk forward while rows still begin above the bottom edge.
  let end = start;
  while (end < rows.length && rows[end]!.y <= bottom) end++;

  return { start, end };
}
