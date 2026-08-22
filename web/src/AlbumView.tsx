import { useState } from "preact/hooks";
import { mediaURL } from "./api";
import {
  albumError,
  albumItems,
  currentAlbum,
  deleteAlbum,
  editing,
  moveItem,
  removeFromAlbum,
  type AlbumMedia,
} from "./albums";
import { openLightbox } from "./viewer";
import { UploadPanel } from "./UploadPanel";
import { AlbumTiles, Breadcrumbs } from "./AlbumTiles";
import { favoritesOnly, filterFavorites } from "./favorites";

/**
 * Browsing albums.
 *
 * One view for both levels of the idea: with no album open it shows the top
 * level, and inside an album it shows that album's sub-albums above its own
 * photos. Splitting "the album list" from "an album" into two screens is what
 * made the old sidebar necessary in the first place.
 */
export function AlbumView() {
  const album = currentAlbum.value;

  if (!album) {
    return (
      <div class="album-view">
        <header class="album-header">
          <Breadcrumbs />
        </header>
        <AlbumTiles parentID={null} />
      </div>
    );
  }

  const shown = filterFavorites(albumItems.value);

  return (
    <div class="album-view">
      <header class="album-header">
        <div>
          <Breadcrumbs />
          {album.description && <p class="desc">{album.description}</p>}
        </div>
        <span class="spacer" />
        <span class="count">
          {album.item_count} {album.item_count === 1 ? "photo" : "photos"}
        </span>
        <button
          class={editing.value ? "primary" : ""}
          onClick={() => (editing.value = !editing.value)}
        >
          {editing.value ? "Done" : "Edit"}
        </button>
      </header>

      {/* Sub-albums sit above the photos: an album can hold both, and the
          nesting is the thing you navigate by. */}
      <AlbumTiles parentID={album.id} />

      {albumError.value && <p class="status error">{albumError.value}</p>}

      {/* Upload and reordering both belong to edit mode. Outside it the album
          is for looking at, and nothing is draggable or droppable. */}
      {editing.value && (
        <>
          <UploadPanel albumID={album.id} />
          <DangerZone albumID={album.id} title={album.title} />
        </>
      )}

      {editing.value && albumItems.value.length > 0 && (
        // Says outright what the × does. An album is a view onto the library,
        // so removing from it keeps the photograph -- but a bare × looks like
        // a delete, and people should not have to try it to find out.
        <p class="edit-hint">
          Drag to reorder. × removes a photo from this album; it stays in your
          library.
        </p>
      )}

      {shown.length === 0 ? (
        <p class="empty">
          {favoritesOnly.value && albumItems.value.length > 0
            ? "No favourites in this album."
            : editing.value
              ? "Drop photos above to add them."
              : "No photos here yet. Use Edit to add some."}
        </p>
      ) : (
        <SortableGrid items={shown} />
      )}
    </div>
  );
}

/**
 * Uniform grid used for both viewing and reordering an album.
 *
 * Square tiles rather than the justified layout of the main timeline: during a
 * drag the target has to be predictable, and rows that reflow as items move
 * between them make dropping a guessing game. It is also unvirtualised, which
 * is what lets a drag scroll across the whole album -- a drop target that does
 * not exist in the DOM cannot be dropped on.
 */
function SortableGrid({ items }: { items: AlbumMedia[] }) {
  const [dragging, setDragging] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const canEdit = editing.value;

  const onDrop = (toIndex: number) => {
    if (dragging === null) return;
    void moveItem(dragging, toIndex);
    setDragging(null);
    setOver(null);
  };

  return (
    <div class={`album-grid ${canEdit ? "editing" : ""}`}>
      {items.map((m, index) => (
        <div
          key={m.id}
          class={`album-tile ${dragging === m.id ? "dragging" : ""} ${
            over === index ? "over" : ""
          }`}
          draggable={canEdit}
          onDragStart={(e) => {
            setDragging(m.id);
            e.dataTransfer?.setData("text/plain", String(m.id));
            if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
          }}
          onDragEnd={() => {
            setDragging(null);
            setOver(null);
          }}
          onDragOver={(e) => {
            if (!canEdit || dragging === null) return;
            // Without preventDefault the browser refuses the drop entirely.
            e.preventDefault();
            if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
            setOver(index);
          }}
          onDragLeave={() => setOver((cur) => (cur === index ? null : cur))}
          onDrop={(e) => {
            e.preventDefault();
            onDrop(index);
          }}
          onClick={() => {
            if (!canEdit) openLightbox(m, items);
          }}
        >
          {m.has_thumb ? (
            <img
              src={mediaURL(m, "thumb")}
              alt={m.filename}
              loading="lazy"
              decoding="async"
              // Images are draggable by default, and that native drag would
              // fight the tile's own. Outside edit mode nothing should drag.
              draggable={false}
            />
          ) : (
            <span class="no-poster">{m.kind === "video" ? "video" : "no preview"}</span>
          )}
          {m.kind === "video" && m.has_thumb && <span class="video-badge">video</span>}
          {canEdit && (
            <button
              class="remove"
              title="Remove from this album (the photo stays in your library)"
              onClick={(e) => {
                e.stopPropagation();
                void removeFromAlbum(m.id);
              }}
            >
              ×
            </button>
          )}
        </div>
      ))}
    </div>
  );
}

function DangerZone({ albumID, title }: { albumID: number; title: string }) {
  const [confirming, setConfirming] = useState(false);

  if (!confirming) {
    return (
      <div class="danger-zone">
        <button onClick={() => setConfirming(true)}>Delete album</button>
      </div>
    );
  }
  return (
    <div class="danger-zone confirming">
      {/* Says plainly that the photographs survive -- otherwise this is a
          frightening button and people will not use albums freely. */}
      <span>
        Delete <strong>{title}</strong>? The photos stay in your library.
      </span>
      <button onClick={() => setConfirming(false)}>Cancel</button>
      <button class="danger" onClick={() => void deleteAlbum(albumID)}>
        Delete
      </button>
    </div>
  );
}
