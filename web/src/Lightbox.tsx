import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { mediaURL, type Media } from "./api";
import { albumItems, currentAlbum, setAlbumCover } from "./albums";
import { setFavorite } from "./favorites";
import { posterBusy, posterError, setPoster } from "./poster";
import { closePhoto, showPhoto } from "./router";
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

export function Lightbox({ media }: { media: Media }) {
  const all = lightboxScope.value;
  const index = all.findIndex((m) => m.id === media.id);

  // Through the router, not a bare signal write: the URL names the photo on
  // screen, and stepping past it without saying so would leave a reload or a
  // copied link pointing at whichever frame the set was entered on.
  const step = (delta: number) => {
    const next = all[index + delta];
    if (next) showPhoto(next.id);
  };

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

      {/* The arrows stay out of the way of the photograph and appear when the
          pointer does. Arrow keys and the edges of the frame both still work
          without them; a permanent pair of chevrons over a picture does not. */}
      {index > 0 && (
        <button class="nav prev" onClick={() => step(-1)} aria-label="Previous">
          ‹
        </button>
      )}
      {index >= 0 && index < all.length - 1 && (
        <button class="nav next" onClick={() => step(1)} aria-label="Next">
          ›
        </button>
      )}

      <div class="stage">
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
