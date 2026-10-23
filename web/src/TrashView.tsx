import { useEffect, useState } from "preact/hooks";
import { mediaURL } from "./api";
import { Breadcrumbs } from "./AlbumTiles";
import { JustifiedGrid } from "./JustifiedGrid";
import { navigate } from "./router";
import {
  daysLeft,
  emptyTrash,
  loadTrash,
  purgeMedia,
  restoreMedia,
  trashed,
  trashError,
} from "./trash";

export function TrashView() {
  const [confirming, setConfirming] = useState(false);

  useEffect(() => {
    void loadTrash();
  }, []);

  const items = trashed.value;

  return (
    <div class="album-view">
      <header class="album-header">
        <div>
          <Breadcrumbs current="Trash" />
          <p class="desc">
            Photos are deleted for good 30 days after you move them here.
          </p>
        </div>
        <span class="spacer" />
        <span class="count">
          {items.length} {items.length === 1 ? "photo" : "photos"}
        </span>
        {items.length > 0 &&
          (confirming ? (
            <>
              <button onClick={() => setConfirming(false)}>Cancel</button>
              <button
                class="danger"
                onClick={() => {
                  void emptyTrash();
                  setConfirming(false);
                }}
              >
                Delete {items.length} for good
              </button>
            </>
          ) : (
            <button onClick={() => setConfirming(true)}>Empty trash</button>
          ))}
      </header>

      {trashError.value && <p class="status error">{trashError.value}</p>}

      {items.length === 0 ? (
        <p class="empty">Nothing here.</p>
      ) : (
        <JustifiedGrid items={items}>
          {(m, style) => (
            <div key={m.id} class="album-tile trashed" style={style}>
              {/* Openable like anywhere else. Deciding whether to restore
                  something from a 256px thumbnail is guesswork, which is why
                  the lightbox not working here was worth fixing. */}
              <button
                class="tile-open"
                title={`Open ${m.filename}`}
                onClick={() => navigate({ view: "trash", photo: m.id })}
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
              </button>
              {/* The deadline comes from the server with each item, so this
                  cannot drift out of step with the retention policy. */}
              <span class="deadline">{daysLeft(m.purge_at)}d left</span>
              <div class="trash-actions">
                <button onClick={() => void restoreMedia(m.id)}>Restore</button>
                <button class="danger" onClick={() => void purgeMedia(m.id)}>
                  Delete
                </button>
              </div>
            </div>
          )}
        </JustifiedGrid>
      )}
    </div>
  );
}
