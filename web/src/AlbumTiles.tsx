import { useState } from "preact/hooks";
import { mediaURL } from "./api";
import {
  albumError,
  albumPath,
  childAlbums,
  createAlbum,
  moveAlbum,
  openAlbum,
  openLibraryRoot,
  type Album,
} from "./albums";
import { view } from "./state";
import { clearSearch } from "./trash";

/**
 * Breadcrumb.
 *
 * Every level is a link, including the root, because the only other way back
 * out of a nested album is the browser's back button -- which this app does not
 * drive.
 */
export function Breadcrumbs() {
  const path = albumPath.value;

  return (
    <nav class="crumbs" aria-label="Album path">
      <button
        class="crumb"
        onClick={() => {
          openLibraryRoot();
          view.value = "album";
          clearSearch();
        }}
      >
        Library
      </button>
      {path.map((a, i) => (
        <span key={a.id}>
          <span class="sep" aria-hidden="true">
            ›
          </span>
          {i === path.length - 1 ? (
            <span class="crumb current">{a.title}</span>
          ) : (
            <button class="crumb" onClick={() => void openAlbum(a.id)}>
              {a.title}
            </button>
          )}
        </span>
      ))}
    </nav>
  );
}

/**
 * The albums at one level, as tiles.
 *
 * Dragging one onto another nests it. The server refuses a move that would
 * make an album its own ancestor, and that refusal is surfaced rather than
 * swallowed -- from the outside, a rejected drag just looks like nothing
 * happened.
 */
export function AlbumTiles({ parentID }: { parentID: number | null }) {
  const children = childAlbums(parentID);
  const [dragging, setDragging] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");

  const submit = async (e: Event) => {
    e.preventDefault();
    const name = title.trim();
    if (!name) return;
    const album = await createAlbum(name, parentID);
    setTitle("");
    setCreating(false);
    if (album) void openAlbum(album.id);
  };

  return (
    <>
      {albumError.value && <p class="status error">{albumError.value}</p>}

      <div class="tiles">
        {/* Always first, always at the top level: the whole library is the one
            place you can be sure holds a given photo. */}
        {parentID === null && (
          <button
            class="tile special"
            onClick={() => {
              view.value = "all";
              openLibraryRoot();
              clearSearch();
            }}
          >
            <span class="tile-art all" aria-hidden="true" />
            <span class="tile-label">
              <span class="tile-title">All photos</span>
            </span>
          </button>
        )}

        {children.map((a) => (
          <AlbumTile
            key={a.id}
            album={a}
            dragging={dragging}
            over={over}
            setDragging={setDragging}
            setOver={setOver}
          />
        ))}

        {creating ? (
          <form class="tile new" onSubmit={submit}>
            <input
              value={title}
              placeholder="Album name"
              autofocus
              onInput={(e) => setTitle((e.target as HTMLInputElement).value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setCreating(false);
                  setTitle("");
                }
              }}
            />
          </form>
        ) : (
          <button class="tile new" onClick={() => setCreating(true)}>
            <span class="tile-art plus" aria-hidden="true">
              +
            </span>
            <span class="tile-label">
              <span class="tile-title">
                {parentID === null ? "New album" : "New sub-album"}
              </span>
            </span>
          </button>
        )}
      </div>
    </>
  );
}

function AlbumTile({
  album,
  dragging,
  over,
  setDragging,
  setOver,
}: {
  album: Album;
  dragging: number | null;
  over: number | null;
  setDragging: (id: number | null) => void;
  setOver: (id: number | null) => void;
}) {
  const cover = album.cover;
  const nested = childAlbums(album.id).length;

  return (
    <button
      class={`tile ${dragging === album.id ? "dragging" : ""} ${
        over === album.id ? "over" : ""
      }`}
      draggable
      onDragStart={(e) => {
        setDragging(album.id);
        e.dataTransfer?.setData("text/plain", String(album.id));
        if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
      }}
      onDragEnd={() => {
        setDragging(null);
        setOver(null);
      }}
      onDragOver={(e) => {
        if (dragging === null || dragging === album.id) return;
        // Without preventDefault the browser refuses the drop outright.
        e.preventDefault();
        if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
        setOver(album.id);
      }}
      onDragLeave={() => setOver(null)}
      onDrop={(e) => {
        e.preventDefault();
        if (dragging !== null && dragging !== album.id) {
          void moveAlbum(dragging, album.id);
        }
        setDragging(null);
        setOver(null);
      }}
      onClick={() => void openAlbum(album.id)}
      title={album.title}
    >
      {cover ? (
        <img
          class="tile-art"
          src={mediaURL(cover, "thumb")}
          alt=""
          loading="lazy"
          decoding="async"
          draggable={false}
        />
      ) : (
        <span class="tile-art empty" aria-hidden="true" />
      )}
      <span class="tile-label">
        <span class="tile-title">{album.title}</span>
        <span class="tile-meta">
          {album.total_count} {album.total_count === 1 ? "photo" : "photos"}
          {nested > 0 && <> · {nested} inside</>}
        </span>
      </span>
    </button>
  );
}
