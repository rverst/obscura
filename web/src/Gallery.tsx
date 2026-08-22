import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "preact/hooks";
import { mediaURL, type Media } from "./api";
import { layout, visibleRange, type Row } from "./layout";
import { hasMore, items, loadError, loading, loadMore } from "./state";
import { openLightbox } from "./viewer";

const TARGET_ROW_HEIGHT = 220;
const MAX_ROW_HEIGHT = 380;
const GAP = 4;

/** Rows rendered beyond the viewport, so scrolling does not reveal blanks. */
const OVERSCAN_PX = 800;

/** How close to the bottom before the next page is fetched. */
const LOAD_THRESHOLD_PX = 1200;

export function Gallery() {
  const scroller = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewport, setViewport] = useState(0);

  // Width and viewport height drive the whole layout, so they are measured
  // rather than assumed, and re-measured on resize and rotation.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const measure = () => {
      const style = getComputedStyle(el);
      const padding =
        parseFloat(style.paddingLeft) + parseFloat(style.paddingRight);
      setWidth(el.clientWidth - padding);
      setViewport(el.clientHeight);
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const onScroll = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    setScrollTop(el.scrollTop);
    if (el.scrollHeight - el.scrollTop - el.clientHeight < LOAD_THRESHOLD_PX) {
      void loadMore();
    }
  }, []);

  const media = items.value;
  const { rows, totalHeight } = layout(media, {
    containerWidth: width,
    targetRowHeight: TARGET_ROW_HEIGHT,
    gap: GAP,
    maxRowHeight: MAX_ROW_HEIGHT,
  });

  // A library that does not fill the first screen would otherwise never trigger
  // the scroll handler, and paging would stall after page one.
  useEffect(() => {
    if (
      !loading.value &&
      hasMore() &&
      totalHeight > 0 &&
      totalHeight < viewport
    ) {
      void loadMore();
    }
  }, [totalHeight, viewport]);

  const { start, end } = visibleRange(rows, scrollTop, viewport, OVERSCAN_PX);
  const visible = rows.slice(start, end);

  return (
    <div class="gallery" ref={scroller} onScroll={onScroll}>
      {media.length === 0 && !loading.value && !loadError.value && (
        <p class="empty">
          No photos yet. Upload some with the API — see the README.
        </p>
      )}

      {/* Full-height spacer: the scrollbar reflects the entire library while
          only the visible rows exist in the DOM. */}
      <div class="grid" style={{ height: `${totalHeight}px` }}>
        {visible.map((row) => (
          <GridRow key={row.y} row={row} />
        ))}
      </div>

      {loading.value && <p class="status">Loading…</p>}
      {loadError.value && <p class="status error">{loadError.value}</p>}
    </div>
  );
}

function GridRow({ row }: { row: Row }) {
  return (
    <>
      {row.items.map((item) => (
        <Tile
          key={item.media.id}
          media={item.media}
          style={{
            left: `${item.x}px`,
            top: `${row.y}px`,
            width: `${item.width}px`,
            height: `${item.height}px`,
          }}
        />
      ))}
    </>
  );
}

/** Runtime as m:ss, or a plain marker when the duration is unknown. */
export function durationLabel(media: Media): string {
  const ms = media.duration_ms ?? 0;
  if (ms <= 0) return "video";
  const total = Math.round(ms / 1000);
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

function Tile({
  media,
  style,
}: {
  media: Media;
  style: Record<string, string>;
}) {
  const [loaded, setLoaded] = useState(false);

  return (
    <button
      class="tile"
      style={style}
      onClick={() => openLightbox(media, items.value)}
      title={media.filename}
    >
      {media.has_thumb ? (
        <img
          src={mediaURL(media, "thumb")}
          alt={media.filename}
          // Native lazy loading on top of virtualisation: a row entering the
          // overscan area is in the DOM but need not fetch immediately.
          loading="lazy"
          decoding="async"
          // Images are draggable by default. Left alone, dragging a photo starts
          // a native image drag that shows a ghost over the grid and interferes
          // with anything else drag-related in the app.
          draggable={false}
          width={media.width}
          height={media.height}
          class={loaded ? "loaded" : ""}
          onLoad={() => setLoaded(true)}
        />
      ) : (
        // A video whose first frames could not be decoded, or that predates
        // ffmpeg being installed.
        <span class="no-poster">video</span>
      )}
      {media.kind === "video" && (
        <span class="video-badge">{durationLabel(media)}</span>
      )}
    </button>
  );
}
