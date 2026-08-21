import { useEffect } from "preact/hooks";
import { mediaURL, type Media } from "./api";
import { closeLightbox, lightbox, lightboxScope } from "./viewer";

export function Lightbox({ media }: { media: Media }) {
  const all = lightboxScope.value;
  const index = all.findIndex((m) => m.id === media.id);

  const step = (delta: number) => {
    const next = all[index + delta];
    if (next) lightbox.value = next;
  };

  // Keyboard navigation is what makes a gallery usable on a desktop; without it
  // reviewing a hundred photos means a hundred round trips to the mouse.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      switch (e.key) {
        case "Escape":
          closeLightbox();
          break;
        case "ArrowLeft":
          step(-1);
          break;
        case "ArrowRight":
          step(1);
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [index, all]);

  return (
    <div
      class="lightbox"
      onClick={(e) => {
        // Clicking the backdrop closes; clicking the photo or the panel must not.
        if (e.target === e.currentTarget) closeLightbox();
      }}
    >
      <button class="close" onClick={closeLightbox}>
        Close
      </button>

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
          <video src={mediaURL(media, "original")} controls autoPlay />
        ) : (
          <img
            // The preview, not the original: a 24 MP JPEG per photo would make
            // browsing painful over wifi for no visible gain on screen.
            src={mediaURL(media, media.has_thumb ? "preview" : "original")}
            alt={media.filename}
          />
        )}
      </div>

      <Info media={media} />
    </div>
  );
}

function Info({ media }: { media: Media }) {
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
