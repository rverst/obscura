import { useState } from "preact/hooks";
import { mediaURL } from "./api";
import {
  albumError,
  albumItems,
  childAlbums,
  currentAlbum,
  deleteAlbum,
  editing,
  indexForDrop,
  moveItem,
  removeFromAlbum,
  type AlbumMedia,
} from "./albums";
import { GearIcon } from "./icons";
import { trashMedia } from "./trash";
import { navigate } from "./router";
import { PhotoPicker } from "./PhotoPicker";
import { UploadPanel } from "./UploadPanel";
import { AlbumTiles, Breadcrumbs } from "./AlbumTiles";
import { JustifiedGrid } from "./JustifiedGrid";
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
  const [picking, setPicking] = useState(false);

  if (!album) {
    return (
      <div class="album-view">
        <header class="album-header">
          <Breadcrumbs />
          <span class="spacer" />
          <EditToggle />
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
        <EditToggle />
      </header>

      {/* Sub-albums sit above the photos: an album can hold both, and the
          nesting is the thing you navigate by. */}
      <AlbumTiles parentID={album.id} />

      {albumError.value && <p class="status error">{albumError.value}</p>}

      {/* Upload and reordering both belong to edit mode. Outside it the album
          is for looking at, and nothing is draggable or droppable. */}
      {editing.value && (
        <>
          {/* Two ways in, because there are two things people mean by "add":
              a file from the camera, and a photograph already in the library. */}
          <p class="add-row">
            <button onClick={() => setPicking(true)}>Add from library</button>
          </p>
          <UploadPanel albumID={album.id} />
          <DangerZone albumID={album.id} title={album.title} />
        </>
      )}

      {picking && <PhotoPicker albumID={album.id} onClose={() => setPicking(false)} />}

      {editing.value && albumItems.value.length > 0 && (
        // Says outright what each of the two does. An album is a view onto the
        // library, so removing from it keeps the photograph -- but a bare ×
        // looks like a delete, and people should not have to try it to find
        // out which one they just pressed.
        <p class="edit-hint">
          Drag to reorder. × takes a photo out of this album and leaves it in
          your library, where “Add from library” can put it back. The bin moves
          it to the trash, kept for thirty days.
        </p>
      )}

      {shown.length === 0 ? (
        // An album that holds only sub-albums is not empty and needs no
        // prompting: the tiles above it are its contents. Saying "no photos
        // here yet" under them reads as though something were missing.
        <EmptyAlbum hasSubAlbums={childAlbums(album.id).length > 0} />
      ) : (
        <SortableGrid items={shown} />
      )}
    </div>
  );
}

/** What to say, if anything, in an album with no photographs of its own. */
function EmptyAlbum({ hasSubAlbums }: { hasSubAlbums: boolean }) {
  if (favoritesOnly.value && albumItems.value.length > 0) {
    return <p class="empty">No favourites in this album.</p>;
  }
  if (editing.value) {
    return <p class="empty">Drop photos above to add them.</p>;
  }
  if (hasSubAlbums) return null;
  return (
    <p class="empty">
      No photos here yet. Use Edit to add some from your library, or upload new
      ones.
    </p>
  );
}

/**
 * The one switch between looking and arranging.
 *
 * Making an album, reordering, uploading and deleting all sit behind it --
 * including at the top level, which had no way into edit mode at all and so
 * showed its one editing control, "New album", to everybody all the time.
 *
 * Exported because the timeline needs the same switch and has no header of its
 * own to put it in; two of these drifting apart would be two answers to "am I
 * editing?".
 */
export function EditToggle() {
  const on = editing.value;
  return (
    <button
      class={`edit-toggle ${on ? "primary" : ""}`}
      aria-pressed={on ? "true" : "false"}
      aria-label={on ? "Done editing" : "Edit"}
      title={on ? "Done editing" : "Edit"}
      onClick={() => (editing.value = !on)}
    >
      <GearIcon />
    </button>
  );
}

/**
 * The grid used for both viewing and reordering an album.
 *
 * Justified rows like the timeline's. Nothing moves while a photo is being
 * dragged -- the order changes only on drop -- so the rows hold still under the
 * pointer and the before/after bar stays a reliable target. It is unvirtualised,
 * which is what lets a drag scroll across the whole album.
 */
function SortableGrid({ items }: { items: AlbumMedia[] }) {
  const [dragging, setDragging] = useState<number | null>(null);
  // Which side of which tile the photo would land on. The albums above use the
  // same bar for the same meaning; an outline round the whole tile said "here,
  // somewhere" and left which side of it to guesswork.
  const [drop, setDrop] = useState<{ index: number; side: "before" | "after" } | null>(null);
  const canEdit = editing.value;

  const onDrop = () => {
    if (dragging !== null && drop) {
      void moveItem(dragging, indexForDrop(items, dragging, drop.index, drop.side));
    }
    setDragging(null);
    setDrop(null);
  };

  return (
    <JustifiedGrid items={items} class={canEdit ? "editing" : ""}>
      {(m, style, index) => (
        <div
          key={m.id}
          style={style}
          class={`album-tile ${dragging === m.id ? "dragging" : ""} ${
            drop?.index === index && dragging !== m.id ? `drop-${drop.side}` : ""
          }`}
          draggable={canEdit}
          onDragStart={(e) => {
            setDragging(m.id);
            e.dataTransfer?.setData("text/plain", String(m.id));
            if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
          }}
          onDragEnd={() => {
            setDragging(null);
            setDrop(null);
          }}
          onDragOver={(e) => {
            if (!canEdit || dragging === null) return;
            // Without preventDefault the browser refuses the drop entirely.
            e.preventDefault();
            if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
            // A plain midpoint split: unlike an album, a photo has no "drop
            // inside me" case to leave room for.
            const box = (e.currentTarget as HTMLElement).getBoundingClientRect();
            const side = e.clientX - box.left < box.width / 2 ? "before" : "after";
            setDrop({ index, side });
          }}
          onDragLeave={() => setDrop((cur) => (cur?.index === index ? null : cur))}
          onDrop={(e) => {
            e.preventDefault();
            onDrop();
          }}
          onClick={() => {
            if (canEdit) return;
            const album = currentAlbum.value;
            if (album) navigate({ view: "album", albumID: album.id, photo: m.id });
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
            <>
              {/* Deleting a photograph happens here rather than in the
                  lightbox: full-screen is for looking at a picture, and a
                  delete one click from the arrow keys is a delete waiting to
                  happen by accident. */}
              <button
                class="to-trash"
                title="Move to trash (kept for thirty days)"
                onClick={(e) => {
                  e.stopPropagation();
                  void trashMedia(m.id);
                }}
              >
                {"\u{1F5D1}"}
              </button>
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
            </>
          )}
        </div>
      )}
    </JustifiedGrid>
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
