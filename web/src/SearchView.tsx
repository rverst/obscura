import { mediaURL } from "./api";
import { durationLabel } from "./Gallery";
import { searching, searchQuery, searchResults } from "./trash";
import { openLightbox } from "./viewer";

/**
 * Search results.
 *
 * A uniform grid rather than the justified timeline layout: results are a
 * handful of items with no chronological shape, and packing them into
 * full-width rows would imply a continuity they do not have.
 */
export function SearchView() {
  const hits = searchResults.value;
  const query = searchQuery.value.trim();

  return (
    <div class="album-view">
      <header class="album-header">
        <div>
          <h2>Search</h2>
          <p class="desc">
            {searching.value
              ? "Searching…"
              : `${hits.length} ${hits.length === 1 ? "result" : "results"} for “${query}”`}
          </p>
        </div>
      </header>

      {!searching.value && hits.length === 0 ? (
        <p class="empty">Nothing matched. Filenames and camera names are searchable.</p>
      ) : (
        <div class="album-grid">
          {hits.map((m) => (
            <button
              key={m.id}
              class="album-tile"
              onClick={() => openLightbox(m, hits)}
              title={m.filename}
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
                <span class="no-poster">{m.kind === "video" ? "video" : "no preview"}</span>
              )}
              {m.kind === "video" && <span class="video-badge">{durationLabel(m)}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
