import { useEffect, useRef, useState } from "preact/hooks";
import { api, mediaURL, type Cursor, type Media } from "./api";
import { addToAlbum, albumItems } from "./albums";
import { durationLabel } from "./Gallery";
import { JustifiedGrid } from "./JustifiedGrid";

/** Smaller rows than the timeline: the sheet is narrower, and choosing wants an overview. */
const PICKER_ROW_HEIGHT = 140;
const PICKER_MAX_ROW_HEIGHT = 240;

/**
 * Putting photographs that are already in the library into an album.
 *
 * The server has taken this request since the beginning; nothing ever asked it.
 * Which meant an album could only be filled by uploading into it, and the ×
 * that takes a photo back out was a one-way door -- the only way to undo it was
 * to upload the file a second time. That asymmetry is why removing from an
 * album felt more dangerous than moving to the trash, which has always had a
 * way back.
 *
 * It pages the library itself rather than reading the timeline's signal: the
 * favourites filter narrows that one, and a picker that silently refused to
 * show non-favourites would be a puzzle. Its own cursor also means scrolling
 * here does not drag the timeline's paging along behind it.
 *
 * It opens on the photos that are in no album yet, because those are what one
 * usually comes here to sort. The toggle widens it to the whole library.
 */
export function PhotoPicker({
  albumID,
  onClose,
}: {
  albumID: number;
  onClose: () => void;
}) {
  const [media, setMedia] = useState<Media[]>([]);
  const [cursor, setCursor] = useState<Cursor | undefined>(undefined);
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [chosen, setChosen] = useState<number[]>([]);
  const [adding, setAdding] = useState(false);
  const [unfiled, setUnfiled] = useState(true);
  // Switching the filter while a page is in flight must not let that page land
  // in the new list.
  const generation = useRef(0);

  // What is already here. Adding is idempotent on the server, but offering to
  // add a photo that is already in the album is offering to do nothing.
  const present = new Set(albumItems.value.map((m) => m.id));

  const loadPage = async (from?: Cursor) => {
    if (from && loading) return;
    const mine = ++generation.current;
    setLoading(true);
    setError("");
    try {
      const page = await api.listMedia(from, 100, { unfiled });
      if (mine !== generation.current) return;
      const fresh = page.items ?? [];
      setMedia((prev) => (from ? [...prev, ...fresh] : fresh));
      setCursor(page.next);
      if (!page.next || fresh.length === 0) setDone(true);
    } catch (err) {
      if (mine !== generation.current) return;
      setError(err instanceof Error ? err.message : "could not read the library");
    } finally {
      if (mine === generation.current) setLoading(false);
    }
  };

  useEffect(() => {
    setMedia([]);
    setCursor(undefined);
    setDone(false);
    void loadPage();
  }, [unfiled]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const toggle = (id: number) => {
    setChosen((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };

  const confirm = async () => {
    if (chosen.length === 0) return;
    setAdding(true);
    await addToAlbum(albumID, chosen);
    setAdding(false);
    onClose();
  };

  return (
    <div
      class="sheet-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <section class="sheet" role="dialog" aria-modal="true" aria-label="Add photos from library">
        <header class="sheet-head">
          <h3>Add from library</h3>
          <span class="spacer" />
          <button
            class={`filter-toggle ${unfiled ? "on" : ""}`}
            aria-pressed={unfiled ? "true" : "false"}
            title={unfiled ? "Showing photos in no album" : "Show only photos in no album"}
            onClick={() => setUnfiled(!unfiled)}
          >
            Not in an album
          </button>
          <button class="close" onClick={onClose} aria-label="Close" title="Close (Esc)">
            <span aria-hidden="true">{"×"}</span>
          </button>
        </header>

        {error && <p class="status error">{error}</p>}

        <div class="sheet-body">
          <JustifiedGrid
            items={media}
            targetRowHeight={PICKER_ROW_HEIGHT}
            maxRowHeight={PICKER_MAX_ROW_HEIGHT}
          >
            {(m, style) => {
              const already = present.has(m.id);
              const picked = chosen.includes(m.id);
              return (
                <button
                  key={m.id}
                  style={style}
                  class={`album-tile ${picked ? "picked" : ""} ${already ? "already" : ""}`}
                  disabled={already}
                  aria-pressed={picked ? "true" : "false"}
                  title={already ? `${m.filename} — already in this album` : m.filename}
                  onClick={() => toggle(m.id)}
                >
                  {m.has_thumb ? (
                    <img
                      src={mediaURL(m, "thumb")}
                      alt={m.filename}
                      loading="lazy"
                      decoding="async"
                      draggable={false}
                    />
                  ) : (
                    <span class="no-poster">
                      {m.kind === "video" ? "video" : "no preview"}
                    </span>
                  )}
                  {m.kind === "video" && m.has_thumb && (
                    <span class="video-badge">{durationLabel(m)}</span>
                  )}
                  {/* Says which of the two reasons a tile is marked: chosen
                      just now, or here already and not on offer. */}
                  {(picked || already) && (
                    <span class="pick-mark" aria-hidden="true">
                      {picked ? "✓" : "·"}
                    </span>
                  )}
                </button>
              );
            }}
          </JustifiedGrid>

          {!done && media.length > 0 && (
            <p class="sheet-more">
              <button onClick={() => void loadPage(cursor)} disabled={loading}>
                {loading ? "Loading…" : "Show older photos"}
              </button>
            </p>
          )}
          {media.length === 0 && !loading && !error && (
            <p class="empty">
              {unfiled
                ? "Every photo is already in an album."
                : "Nothing in the library yet. Upload some photos first."}
            </p>
          )}
        </div>

        <footer class="sheet-foot">
          <span class="count">
            {chosen.length === 0
              ? "Nothing selected"
              : `${chosen.length} selected`}
          </span>
          <span class="spacer" />
          <button onClick={onClose}>Cancel</button>
          <button class="primary" disabled={chosen.length === 0 || adding} onClick={confirm}>
            {adding ? "Adding…" : "Add to album"}
          </button>
        </footer>
      </section>
    </div>
  );
}
