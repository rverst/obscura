import { useMemo, useState } from "preact/hooks";
import { mediaURL, type Media } from "./api";
import {
  albumError,
  albumPath,
  childAlbums,
  createAlbum,
  draggingAlbum,
  editing,
  moveAlbum,
  parentOf,
  reorderAlbum,
  type Album,
} from "./albums";
import { navigate } from "./router";
import { items } from "./state";

/**
 * Breadcrumb.
 *
 * Every level is a link, including the root, because the only other way back
 * out of a nested album is the browser's back button -- which this app does not
 * drive.
 *
 * Each crumb is also where a dragged album is dropped to move it *out*. Tiles
 * only ever nested albums deeper; without this the only way back up a level was
 * to delete the album and make it again.
 */
export function Breadcrumbs({ current }: { current?: string }) {
  const path = albumPath.value;

  return (
    <nav class="crumbs" aria-label="Album path">
      <Crumb to={null} label="Library" />
      {path.map((a, i) => (
        <span key={a.id}>
          <span class="sep" aria-hidden="true">
            ›
          </span>
          {i === path.length - 1 ? (
            <span class="crumb current">{a.title}</span>
          ) : (
            <Crumb to={a.id} label={a.title} />
          )}
        </span>
      ))}
      {/* A view that is not an album -- the timeline, the trash, results --
          still says where it sits, and offers the way back that the brand
          button was previously the only source of. */}
      {current && (
        <span>
          <span class="sep" aria-hidden="true">
            ›
          </span>
          <span class="crumb current">{current}</span>
        </span>
      )}
    </nav>
  );
}

/** One crumb: a link, and a drop target that lifts an album to that level. */
function Crumb({ to, label }: { to: number | null; label: string }) {
  const [over, setOver] = useState(false);
  const dragging = draggingAlbum.value;
  // Dropping an album on the level it already sits in is a move to nowhere.
  const takes = dragging !== null && dragging !== to && parentOf(dragging) !== to;

  return (
    <button
      class={`crumb ${over ? "over" : ""}`}
      onClick={() => navigate({ view: "album", albumID: to, photo: null })}
      onDragOver={(e) => {
        if (!takes) return;
        e.preventDefault();
        if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        if (takes && dragging !== null) void moveAlbum(dragging, to);
      }}
    >
      {label}
    </button>
  );
}

/**
 * Where a dragged album would land if it were dropped now.
 *
 * One drag does two jobs: dropped on the middle of a tile the album nests
 * inside it, dropped near an edge it takes a place beside it. Which one is
 * meant has to be visible before the mouse button comes up, hence the mode
 * travelling with the tile it applies to.
 */
type Drop = { id: number; mode: "into" | "before" | "after" };

/** How much of a tile's width, at each edge, means "beside" rather than "inside". */
const EDGE_FRACTION = 0.28;

/** Thumbnails in the "All photos" mosaic. Two by two; nine would be noise. */
const MOSAIC_TILES = 4;

/**
 * The albums at one level, as tiles.
 *
 * Arranging them is edit-mode work: outside it nothing drags, and there is no
 * tile for making a new album. A permanent "New album" tile sat in front of the
 * real albums on every visit, which is a lot of prominence for something done
 * once a season.
 */
export function AlbumTiles({ parentID }: { parentID: number | null }) {
  const children = childAlbums(parentID);
  const canEdit = editing.value;
  const dragging = draggingAlbum.value;
  const [drop, setDrop] = useState<Drop | null>(null);
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");

  const submit = async (e: Event) => {
    e.preventDefault();
    const name = title.trim();
    if (!name) return;
    const album = await createAlbum(name, parentID);
    setTitle("");
    setCreating(false);
    if (album) navigate({ view: "album", albumID: album.id, photo: null });
  };

  const finishDrag = () => {
    draggingAlbum.value = null;
    setDrop(null);
  };

  const onDrop = (target: Album) => {
    if (dragging === null || dragging === target.id || !drop) return finishDrag();
    switch (drop.mode) {
      case "into":
        void moveAlbum(dragging, target.id);
        break;
      case "before":
        void reorderAlbum(dragging, null, target.id);
        break;
      case "after":
        void reorderAlbum(dragging, target.id, null);
        break;
    }
    finishDrag();
  };

  return (
    <>
      {albumError.value && <p class="status error">{albumError.value}</p>}

      <div class="tiles">
        {/* Always first, always at the top level: the whole library is the one
            place you can be sure holds a given photo. */}
        {parentID === null && <AllPhotosTile />}

        {children.map((a) => (
          <AlbumTile
            key={a.id}
            album={a}
            canEdit={canEdit}
            dragging={dragging}
            drop={drop}
            setDrop={setDrop}
            onDrop={onDrop}
            onDragEnd={finishDrag}
          />
        ))}

        {canEdit &&
          (creating ? (
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
          ))}
      </div>

      {/* Only at the top level. Inside an album, "no sub-albums" is the normal
          case and saying so is a line of text where the photos should be. */}
      {!canEdit && parentID === null && children.length === 0 && (
        <p class="empty">No albums yet. Use Edit to make one.</p>
      )}

      {canEdit && children.length > 1 && (
        // Two gestures on one tile is not guessable, and the wrong one of the
        // two is tedious to undo by hand.
        <p class="edit-hint">
          Drag an album to its edge neighbours to reorder, onto the middle of
          another to nest it inside, or onto a breadcrumb to move it out.
        </p>
      )}
    </>
  );
}

/**
 * The tile for the whole library.
 *
 * A mosaic of what is actually in there rather than a gradient: every other
 * tile shows its contents, and this one was the only one that showed a colour.
 * The photos come from the loaded timeline, which boot() has already fetched --
 * so this costs no request.
 */
function AllPhotosTile() {
  const media = items.value;
  const sample = useMemo(() => pickSpread(media, MOSAIC_TILES), [media.length]);

  return (
    <button class="tile special" onClick={() => navigate({ view: "all", photo: null })}>
      {sample.length === MOSAIC_TILES ? (
        <span class="tile-art mosaic" aria-hidden="true">
          {sample.map((m) => (
            <img key={m.id} src={mediaURL(m, "thumb")} alt="" loading="lazy" decoding="async" />
          ))}
        </span>
      ) : (
        // Too few photos to make a mosaic out of; the gradient still says
        // "everything" without pretending there is more here than there is.
        <span class="tile-art all" aria-hidden="true" />
      )}
      <span class="tile-label">
        <span class="tile-title">All photos</span>
      </span>
    </button>
  );
}

/**
 * Picks n thumbnails spread across the library rather than the first n.
 *
 * The newest four photos are usually four frames of the same thing. Spreading
 * the picks over what is loaded gives a mosaic that looks like a library.
 */
function pickSpread(media: Media[], n: number): Media[] {
  const usable = media.filter((m) => m.has_thumb);
  if (usable.length < n) return [];
  const step = usable.length / n;
  return Array.from({ length: n }, (_, i) => usable[Math.floor(i * step)]!);
}

function AlbumTile({
  album,
  canEdit,
  dragging,
  drop,
  setDrop,
  onDrop,
  onDragEnd,
}: {
  album: Album;
  canEdit: boolean;
  dragging: number | null;
  drop: Drop | null;
  setDrop: (drop: Drop | null) => void;
  onDrop: (album: Album) => void;
  onDragEnd: () => void;
}) {
  const cover = album.cover;
  const nested = childAlbums(album.id).length;
  const mode = drop?.id === album.id ? drop.mode : null;
  // Where "out" leads: the level above the one this album is on.
  const outOf = album.parent_id === undefined ? null : parentOf(album.parent_id);

  return (
    <button
      class={`tile ${dragging === album.id ? "dragging" : ""} ${
        mode === "into" ? "over" : mode ? `drop-${mode}` : ""
      }`}
      draggable={canEdit}
      onDragStart={(e) => {
        draggingAlbum.value = album.id;
        e.dataTransfer?.setData("text/plain", String(album.id));
        if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
      }}
      onDragEnd={onDragEnd}
      onDragOver={(e) => {
        if (!canEdit || dragging === null || dragging === album.id) return;
        // Without preventDefault the browser refuses the drop outright.
        e.preventDefault();
        if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
        const box = (e.currentTarget as HTMLElement).getBoundingClientRect();
        const x = e.clientX - box.left;
        const edge = box.width * EDGE_FRACTION;
        setDrop({
          id: album.id,
          mode: x < edge ? "before" : x > box.width - edge ? "after" : "into",
        });
      }}
      onDragLeave={() => {
        if (drop?.id === album.id) setDrop(null);
      }}
      onDrop={(e) => {
        e.preventDefault();
        onDrop(album);
      }}
      onClick={() => navigate({ view: "album", albumID: album.id, photo: null })}
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
      {/* The same move the breadcrumb does, without the drag -- and reachable
          on a touch screen, where there is no drag to speak of. */}
      {canEdit && album.parent_id !== undefined && (
        <button
          class="move-out"
          title="Move out one level"
          aria-label="Move out one level"
          onClick={(e) => {
            e.stopPropagation();
            void moveAlbum(album.id, outOf);
          }}
        >
          ⤴
        </button>
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

