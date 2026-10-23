import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { mediaURL, type Media } from "./api";
import { editing } from "./albums";
import { layout, visibleRange, type Row } from "./layout";
import {
  hasMore,
  items,
  loadError,
  loading,
  loadMore,
  setUnfiledOnly,
  unfiledOnly,
} from "./state";
import { Breadcrumbs } from "./AlbumTiles";
import { GAP, MAX_ROW_HEIGHT, TARGET_ROW_HEIGHT, useContainerSize } from "./JustifiedGrid";
import { navigate } from "./router";
import { trashMedia } from "./trash";

/** Rows rendered beyond the viewport, so scrolling does not reveal blanks. */
const OVERSCAN_PX = 800;

/** How close to the bottom before the next page is fetched. */
const LOAD_THRESHOLD_PX = 1200;

export function Gallery() {
  const scroller = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const { width, height: viewport } = useContainerSize(scroller);

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
    if (!loading.value && hasMore() && totalHeight > 0 && totalHeight < viewport) {
      void loadMore();
    }
  }, [totalHeight, viewport]);

  const { start, end } = visibleRange(rows, scrollTop, viewport, OVERSCAN_PX);
  const visible = rows.slice(start, end);

  return (
    <>
      {/* The timeline had no way back to the albums except the brand button,
          which does not look like navigation. */}
      <header class="album-header view-header">
        <Breadcrumbs current="All photos" />
        <span class="spacer" />
        {/* What is still waiting to be sorted into an album. */}
        <button
          class={`filter-toggle ${unfiledOnly.value ? "on" : ""}`}
          aria-pressed={unfiledOnly.value ? "true" : "false"}
          title={unfiledOnly.value ? "Showing photos in no album" : "Show only photos in no album"}
          onClick={() => void setUnfiledOnly(!unfiledOnly.value)}
        >
          Not in an album
        </button>
      </header>

      <div class="gallery" ref={scroller} onScroll={onScroll}>
        {media.length === 0 && !loading.value && !loadError.value && (
          <p class="empty">
            {unfiledOnly.value
              ? "Every photo is in an album."
              : "No photos yet. Upload some with the API — see the README."}
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
    </>
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

/**
 * One photo in the timeline.
 *
 * A div with a button role rather than a real button, because in edit mode it
 * holds one -- and a button inside a button is not something a browser will
 * lay out, never mind read out.
 */
function Tile({ media, style }: { media: Media; style: Record<string, string> }) {
  const [loaded, setLoaded] = useState(false);
  const canEdit = editing.value;

  const open = () => {
    if (!canEdit) navigate({ view: "all", photo: media.id });
  };

  return (
    <div
      class={`tile ${canEdit ? "editing" : ""}`}
      style={style}
      role="button"
      tabIndex={0}
      onClick={open}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          open();
        }
      }}
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
      {media.kind === "video" && <span class="video-badge">{durationLabel(media)}</span>}
      {canEdit && (
        // The only way to delete a photograph that is in no album. It lives
        // behind Edit for the same reason it left the lightbox: browsing and
        // deleting should not share a click target.
        <button
          class="to-trash"
          title="Move to trash (kept for thirty days)"
          onClick={(e) => {
            e.stopPropagation();
            void trashMedia(media.id);
          }}
        >
          {"\u{1F5D1}"}
        </button>
      )}
    </div>
  );
}
