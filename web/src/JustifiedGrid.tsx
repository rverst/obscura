import type { ComponentChildren, RefObject } from "preact";
import { useLayoutEffect, useRef, useState } from "preact/hooks";
import type { Media } from "./api";
import { layout } from "./layout";

/** Row sizing shared by every photo grid, so they all read as one library. */
export const TARGET_ROW_HEIGHT = 220;
export const MAX_ROW_HEIGHT = 380;
export const GAP = 4;

/**
 * The content box of an element, re-measured on resize and rotation.
 *
 * Width and height drive the whole justified layout, so they are measured
 * rather than assumed. Padding is taken off the width because rows are laid
 * out inside it.
 */
export function useContainerSize(ref: RefObject<HTMLElement | null>): { width: number; height: number } {
  const [size, setSize] = useState({ width: 0, height: 0 });

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const style = getComputedStyle(el);
      const padding = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight);
      setSize({ width: el.clientWidth - padding, height: el.clientHeight });
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  return size;
}

/**
 * The timeline's justified rows, for the grids that are not virtualised.
 *
 * Albums, the trash and the library picker all used to crop every photo to a
 * square. Each one now packs whole photographs into rows the way "All photos"
 * does. Every tile stays in the DOM, which is what lets an album drag reach a
 * photo at the far end: a drop target that is not rendered cannot be dropped on.
 */
export function JustifiedGrid<T extends Media>({
  items,
  class: className = "",
  targetRowHeight = TARGET_ROW_HEIGHT,
  maxRowHeight = MAX_ROW_HEIGHT,
  children,
}: {
  items: T[];
  class?: string;
  targetRowHeight?: number;
  maxRowHeight?: number;
  /** Renders one tile, absolutely positioned by `style`. */
  children: (item: T, style: Record<string, string>, index: number) => ComponentChildren;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const { width } = useContainerSize(ref);
  const { rows, totalHeight } = layout(items, {
    containerWidth: width,
    targetRowHeight,
    gap: GAP,
    maxRowHeight,
  });

  // layout() hands back the same objects it was given, in order, so a running
  // count recovers each one's index for the drag-and-drop arithmetic.
  let index = 0;
  return (
    <div ref={ref} class={`album-grid ${className}`} style={{ height: `${totalHeight}px` }}>
      {rows.flatMap((row) =>
        row.items.map((laid) =>
          children(
            laid.media as T,
            {
              left: `${laid.x}px`,
              top: `${row.y}px`,
              width: `${laid.width}px`,
              height: `${laid.height}px`,
            },
            index++,
          ),
        ),
      )}
    </div>
  );
}
