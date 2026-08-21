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
import { lightbox } from "./state";
import { UploadPanel } from "./UploadPanel";

export function AlbumView() {
  const album = currentAlbum.value;
  if (!album) return <p class="status">Select an album.</p>;

  return (
    <div class="album-view">
      <header class="album-header">
        <div>
          <h2>{album.title}</h2>
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

      {albumError.value && <p class="status error">{albumError.value}</p>}

      {/* Upload and reordering both belong to edit mode. Outside it the album
          is for looking at, and nothing is draggable or droppable. */}
      {editing.value && (
        <>
          <UploadPanel albumID={album.id} />
          <DangerZone albumID={album.id} title={album.title} />
        </>
      )}

      {albumItems.value.length === 0 ? (
        <p class="empty">
          {editing.value
            ? "Drop photos above to add them."
            : "This album is empty. Use Edit to add photos."}
        </p>
      ) : (
        <SortableGrid items={albumItems.value} />
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
            if (!canEdit) lightbox.value = m;
          }}
        >
          <img
            src={mediaURL(m.id, "thumb")}
            alt={m.filename}
            loading="lazy"
            decoding="async"
            // Images are draggable by default, and that native drag would fight
            // the tile's own. Outside edit mode nothing should drag at all.
            draggable={false}
          />
          {canEdit && (
            <button
              class="remove"
              title="Remove from album"
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
