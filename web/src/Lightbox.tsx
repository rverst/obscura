import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { mediaURL, type Media } from "./api";
import { albumItems, currentAlbum, setAlbumCover } from "./albums";
import { setFavorite } from "./favorites";
import { posterBusy, posterError, setPoster } from "./poster";
import { FullscreenIcon } from "./icons";
import { closePhoto, stepLightbox } from "./router";
import { timelineTotal, view } from "./state";
import { purgeMedia, restoreMedia } from "./trash";
import { handleVideoKey, trackFrames } from "./videokeys";
import {
  infoPinned,
  isTrashed,
  lightboxScope,
  loadVolume,
  saveVolume,
  toggleInfo,
} from "./viewer";

/** How long the pointer must sit still before the controls step out of the way. */
const IDLE_MS = 2500;

/**
 * Hides the chrome once the pointer stops moving.
 *
 * Hovering already hid the arrows, but a lightbox fills the window -- so as
 * long as the mouse was anywhere at all, "hovering" was always true and nothing
 * ever went away. Stillness is the signal that someone is looking at the
 * photograph rather than working the controls.
 */
function useIdle(): boolean {
  const [idle, setIdle] = useState(false);

  useEffect(() => {
    // Not `number`: @types/node is in scope for the test helpers, so bare
    // setTimeout types as Node's.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const wake = () => {
      setIdle(false);
      clearTimeout(timer);
      timer = setTimeout(() => setIdle(true), IDLE_MS);
    };
    wake();

    const events = ["pointermove", "pointerdown", "keydown", "wheel", "touchstart"];
    for (const name of events) window.addEventListener(name, wake, { passive: true });
    return () => {
      clearTimeout(timer);
      for (const name of events) window.removeEventListener(name, wake);
    };
  }, []);

  return idle;
}

/** The fullscreen API, with Safari's prefixed spelling as the fallback. */
type FullscreenDocument = Document & {
  webkitFullscreenEnabled?: boolean;
  webkitFullscreenElement?: Element | null;
  webkitExitFullscreen?: () => Promise<void>;
};
type FullscreenElement = HTMLElement & { webkitRequestFullscreen?: () => Promise<void> };

function fullscreenElement(): Element | null {
  const doc = document as FullscreenDocument;
  return doc.fullscreenElement ?? doc.webkitFullscreenElement ?? null;
}

/**
 * Whether this browser can take an element full screen at all.
 *
 * Safari on the iPhone cannot (only a <video> can), so the button is not
 * offered there; adding the app to the home screen is the way to lose the
 * browser chrome on that one.
 */
function fullscreenSupported(): boolean {
  const doc = document as FullscreenDocument;
  return !!(doc.fullscreenEnabled ?? doc.webkitFullscreenEnabled);
}

function setFullscreen(on: boolean): void {
  const doc = document as FullscreenDocument;
  if (on) {
    // The whole document rather than the lightbox element: the lightbox is
    // re-rendered from photo to photo, and the page underneath must stay
    // full screen across all of them.
    const el = document.documentElement as FullscreenElement;
    const request = el.requestFullscreen?.bind(el) ?? el.webkitRequestFullscreen?.bind(el);
    void request?.().catch(() => undefined);
  } else if (fullscreenElement()) {
    const exit = doc.exitFullscreen?.bind(doc) ?? doc.webkitExitFullscreen?.bind(doc);
    void exit?.().catch(() => undefined);
  }
}

/**
 * Full screen for as long as the lightbox is open.
 *
 * Follows the browser's own state, since Esc and the system gestures leave
 * full screen without asking us, and leaves it when the photo is closed --
 * a grid of thumbnails is not what anyone went full screen for.
 */
function useFullscreen(): [boolean, () => void] {
  const [on, setOn] = useState(() => !!fullscreenElement());

  useEffect(() => {
    const sync = () => setOn(!!fullscreenElement());
    document.addEventListener("fullscreenchange", sync);
    document.addEventListener("webkitfullscreenchange", sync);
    return () => {
      document.removeEventListener("fullscreenchange", sync);
      document.removeEventListener("webkitfullscreenchange", sync);
      setFullscreen(false);
    };
  }, []);

  return [on, () => setFullscreen(!fullscreenElement())];
}

/** How far a finger has to travel sideways before it counts as a swipe. */
const SWIPE_PX = 50;

/**
 * Swiping sideways on the photo steps to the next or previous one.
 *
 * Touch and pen only: a mouse drag over a photograph is someone selecting or
 * missing a click, not asking to move on. The gesture has to be clearly more
 * sideways than vertical, and a second finger (a pinch) cancels it.
 */
function useSwipe(onSwipe: (delta: 1 | -1) => void) {
  const start = useRef<{ id: number; x: number; y: number } | null>(null);
  const fingers = useRef(0);

  return {
    onPointerDown: (e: PointerEvent) => {
      if (e.pointerType === "mouse") return;
      fingers.current++;
      start.current = fingers.current === 1 ? { id: e.pointerId, x: e.clientX, y: e.clientY } : null;
    },
    onPointerUp: (e: PointerEvent) => {
      if (e.pointerType === "mouse") return;
      fingers.current = Math.max(0, fingers.current - 1);
      const from = start.current;
      if (!from || from.id !== e.pointerId) return;
      start.current = null;
      const dx = e.clientX - from.x;
      const dy = e.clientY - from.y;
      if (Math.abs(dx) >= SWIPE_PX && Math.abs(dx) > Math.abs(dy) * 1.5) {
        // Finger moves left, the next photo comes in from the right.
        onSwipe(dx < 0 ? 1 : -1);
      }
    },
    onPointerCancel: () => {
      fingers.current = Math.max(0, fingers.current - 1);
      start.current = null;
    },
  };
}

export function Lightbox({ media }: { media: Media }) {
  const all = lightboxScope.value;
  const index = all.findIndex((m) => m.id === media.id);

  // Through the router, not a bare signal write: the URL names the photo on
  // screen, and stepping past it without saying so would leave a reload or a
  // copied link pointing at whichever frame the set was entered on.
  const step = (delta: 1 | -1) => void stepLightbox(delta);
  // A scope of one is a photo opened on its own, from a link to somewhere the
  // timeline has not been scrolled to; there is nothing to step to.
  const canStep = index >= 0 && all.length > 1;

  // The timeline is paged, so its loaded window is not the whole of it. The
  // server's count is; the max covers an upload that has landed since.
  const total =
    view.value === "all" && timelineTotal.value !== null
      ? Math.max(timelineTotal.value, all.length)
      : all.length;

  const [fullscreen, toggleFullscreen] = useFullscreen();
  const swipe = useSwipe(step);

  const trashed = isTrashed(media);
  const idle = useIdle();
  const video = useRef<HTMLVideoElement>(null);

  // Every clip used to start at full volume, however quiet the last one was
  // left. Set before playback gets going, and again per clip, since stepping
  // to the next one keeps the element and swaps only its source.
  useEffect(() => {
    const el = video.current;
    const saved = loadVolume();
    if (!el || !saved) return;
    el.volume = saved.volume;
    el.muted = saved.muted;
  }, [media.id]);

  // Per clip: a new source can have a different frame rate.
  const frames = useRef<ReturnType<typeof trackFrames> | null>(null);
  useEffect(() => {
    const el = video.current;
    if (!el) return;
    const tracker = trackFrames(el);
    frames.current = tracker;
    return () => {
      tracker.stop();
      frames.current = null;
    };
  }, [media.id, media.kind]);

  // Only offered where it means something: the photo has to be in the album
  // whose cover would change. The server enforces the same rule.
  const album = currentAlbum.value;
  const inAlbum = !!album && albumItems.value.some((m) => m.id === media.id);
  const isCover = album?.cover_media_id === media.id;

  // Keyboard navigation is what makes a gallery usable on a desktop; without it
  // reviewing a hundred photos means a hundred round trips to the mouse.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = video.current;
      if (el && handleVideoKey(el, e, frames.current?.frame() ?? 1 / 30)) {
        e.preventDefault();
        return;
      }
      switch (e.key) {
        case "Escape":
          closePhoto();
          break;
        case "ArrowLeft":
          step(-1);
          break;
        case "ArrowRight":
          step(1);
          break;
        case "i":
          toggleInfo();
          break;
        case "f":
          // Reviewing a shoot is exactly when marking should not require
          // aiming at a button.
          if (!trashed) void setFavorite(media.id, !media.favorite);
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [index, all, media.id, media.favorite, trashed]);

  return (
    <div
      class={`lightbox ${infoPinned.value ? "" : "no-info"} ${idle ? "idle" : ""}`}
      onClick={(e) => {
        // Clicking the backdrop closes; clicking the photo or the panel must not.
        if (e.target === e.currentTarget) closePhoto();
      }}
    >
      {/* Three marks and nothing else: the picture is what one came for, and
          every control here is one that cannot be reached anywhere else.
          Deleting can -- it lives in the grid, behind Edit. */}
      <div class="lightbox-bar">
        {/* A photo opened from the trash offers the two things that make sense
            there. Showing "Delete" for something already deleted was the
            reported confusion, and moving it to the trash again is not an
            operation that exists. */}
        {trashed ? (
          <>
            <button onClick={() => void restoreMedia(media.id)}>Restore</button>
            <button class="danger" onClick={() => void purgeMedia(media.id)}>
              Delete for good
            </button>
          </>
        ) : (
          <button
            class={`fav ${media.favorite ? "on" : ""}`}
            title={media.favorite ? "Remove from favourites (f)" : "Favourite (f)"}
            aria-pressed={media.favorite ? "true" : "false"}
            onClick={() => void setFavorite(media.id, !media.favorite)}
          >
            {media.favorite ? "\u2665" : "\u2661"}
          </button>
        )}
        <span class="spacer" />
        {/* Where in the set this photo is. Quiet, and gone with the rest of
            the bar once the pointer settles. */}
        {canStep && (
          <span class="position" aria-label={`Photo ${index + 1} of ${total}`}>
            {index + 1} / {total}
          </span>
        )}
        {fullscreenSupported() && (
          <button
            class={`fullscreen ${fullscreen ? "on" : ""}`}
            title={fullscreen ? "Leave full screen" : "Full screen"}
            aria-label={fullscreen ? "Leave full screen" : "Full screen"}
            aria-pressed={fullscreen ? "true" : "false"}
            onClick={toggleFullscreen}
          >
            <FullscreenIcon on={fullscreen} />
          </button>
        )}
        <button
          class={`info-toggle ${infoPinned.value ? "on" : ""}`}
          title={infoPinned.value ? "Hide details (i)" : "Show details (i)"}
          aria-label={infoPinned.value ? "Hide details" : "Show details"}
          aria-pressed={infoPinned.value ? "true" : "false"}
          onClick={toggleInfo}
        >
          {/* The circle is the button itself, drawn in CSS -- a glyph like
              (i) is at the mercy of whatever font the platform substitutes. */}
          <span aria-hidden="true">i</span>
        </button>
        <button class="close" onClick={closePhoto} aria-label="Close" title="Close (Esc)">
          <span aria-hidden="true">{"\u00D7"}</span>
        </button>
      </div>

      {/* A video's own controls need sideways drags for scrubbing. */}
      <div class="stage" {...(media.kind === "video" || !canStep ? {} : swipe)}>
        {/* The arrows stay out of the way of the photograph and appear when the
            pointer does. Arrow keys and the edges of the frame both still work
            without them; a permanent pair of chevrons over a picture does not.
            Both are always there once there is a set: it wraps round. */}
        {canStep && (
          <>
            <button class="nav prev" onClick={() => step(-1)} aria-label="Previous">
              <span aria-hidden="true">‹</span>
            </button>
            <button class="nav next" onClick={() => step(1)} aria-label="Next">
              <span aria-hidden="true">›</span>
            </button>
          </>
        )}

        {media.kind === "video" ? (
          // Served straight from the encrypted blob with range support, so
          // seeking works without downloading the whole file first.
          <video
            ref={video}
            src={mediaURL(media, "original")}
            // The chosen still while the first frames load, rather than black.
            poster={media.has_thumb ? mediaURL(media, "preview") : undefined}
            controls
            autoPlay
            // Safari on iOS ignores volume set from a page, but honours muted.
            onVolumeChange={(e) => {
              const el = e.currentTarget;
              saveVolume({ volume: el.volume, muted: el.muted });
            }}
          />
        ) : (
          <img
            // The preview, not the original: a 24 MP JPEG per photo would make
            // browsing painful over wifi for no visible gain on screen.
            src={mediaURL(media, media.has_thumb ? "preview" : "original")}
            alt={media.filename}
          />
        )}
      </div>

      {infoPinned.value && (
        <Info
          media={media}
          canCover={inAlbum}
          isCover={isCover}
          video={media.kind === "video" && !trashed ? video : null}
        />
      )}
    </div>
  );
}

function Info({
  media,
  canCover,
  isCover,
  video,
}: {
  media: Media;
  canCover: boolean;
  isCover: boolean;
  /** The player, when the still can be chosen from it. */
  video: RefObject<HTMLVideoElement | null> | null;
}) {
  const cutting = posterBusy.value === media.id;
  const exif = media.exif;
  const taken = media.taken_at ?? media.created_at;

  return (
    <aside class="info">
      <h3>{media.filename}</h3>
      <dl>
        <dt>{media.taken_at ? "Taken" : "Added"}</dt>
        <dd>{formatDate(taken)}</dd>

        <dt>Dimensions</dt>
        <dd>
          {media.width} × {media.height}
          {megapixels(media) && <> · {megapixels(media)}</>}
        </dd>

        <dt>Size</dt>
        <dd>{formatBytes(media.bytes)}</dd>

        {exif?.make && (
          <>
            <dt>Camera</dt>
            <dd>{[exif.make, exif.model].filter(Boolean).join(" ")}</dd>
          </>
        )}
        {exif?.lens && (
          <>
            <dt>Lens</dt>
            <dd>{exif.lens}</dd>
          </>
        )}
        {(exif?.f_number || exif?.exposure || exif?.iso || exif?.focal_length) && (
          <>
            <dt>Exposure</dt>
            <dd>{formatExposure(exif)}</dd>
          </>
        )}
        {media.gps && (
          <>
            <dt>Location</dt>
            <dd>
              {media.gps.lat.toFixed(5)}, {media.gps.lon.toFixed(5)}
            </dd>
          </>
        )}
      </dl>

      {/* Choosing a cover is a fact about the album, not about the photograph
          on screen, so it sits with the rest of the detail rather than among
          the three marks over the picture. */}
      {canCover && (
        <button
          class={`cover-button ${isCover ? "on" : ""}`}
          aria-pressed={isCover ? "true" : "false"}
          onClick={() => void setAlbumCover(isCover ? null : media.id)}
        >
          {isCover ? "Album cover — click to choose automatically" : "Use as album cover"}
        </button>
      )}

      {/* The automatic still is a frame one second in, which for a clip that
          opens on a pan is often a blur. Pausing on a better moment and
          taking that one is the way to pick, so the button reads the player. */}
      {video && (
        <>
          <button
            class="cover-button"
            disabled={cutting}
            title="Pause on the moment you want. Shift+J / Shift+L step one frame."
            onClick={() => {
              const el = video.current;
              if (el) void setPoster(media.id, el.currentTime * 1000);
            }}
          >
            {cutting ? "Setting thumbnail…" : "Use this frame as thumbnail"}
          </button>
          {posterError.value?.id === media.id && <p class="error">{posterError.value.message}</p>}
        </>
      )}

      <p style="margin-top:20px">
        <a href={mediaURL(media, "original")} download={media.filename}>
          Download original
        </a>
      </p>
    </aside>
  );
}

function megapixels(m: Media): string | null {
  if (!m.width || !m.height) return null;
  const mp = (m.width * m.height) / 1_000_000;
  return mp >= 1 ? `${mp.toFixed(1)} MP` : null;
}

function formatExposure(e: NonNullable<Media["exif"]>): string {
  const parts: string[] = [];
  if (e.focal_length) parts.push(`${Math.round(e.focal_length)}mm`);
  if (e.f_number) parts.push(`ƒ/${e.f_number}`);
  if (e.exposure) parts.push(`${e.exposure}s`);
  if (e.iso) parts.push(`ISO ${e.iso}`);
  return parts.join(" · ");
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let value = n / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
}

function formatDate(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}
