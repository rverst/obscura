import { signal } from "@preact/signals";
import { useState } from "preact/hooks";
import { albums, createAlbum, currentAlbum, openAlbum } from "./albums";
import { view } from "./state";
import { clearSearch, runSearch, searchQuery } from "./trash";

const COLLAPSED_KEY = "obscura.sidebar.collapsed";

/**
 * Collapsed state is remembered across sessions.
 *
 * localStorage rather than a cookie or the server: it is a display preference,
 * not data, and sending it to the server would put a per-device choice into a
 * shared account. Reading is guarded because private browsing modes can make
 * localStorage throw rather than simply be empty.
 */
function loadCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSED_KEY) === "1";
  } catch {
    return false;
  }
}

export const collapsed = signal(loadCollapsed());

export function toggleSidebar(): void {
  collapsed.value = !collapsed.value;
  try {
    localStorage.setItem(COLLAPSED_KEY, collapsed.value ? "1" : "0");
  } catch {
    // A browser that refuses storage still gets a working toggle, it just
    // forgets between visits.
  }
}

export function Sidebar() {
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");

  if (collapsed.value) return null;

  const submit = async (e: Event) => {
    e.preventDefault();
    const name = title.trim();
    if (!name) return;
    const album = await createAlbum(name);
    setTitle("");
    setCreating(false);
    if (album) {
      view.value = "album";
      clearSearch();
      void openAlbum(album.id);
    }
  };

  return (
    <nav class="sidebar">
      <input
        class="search"
        type="search"
        placeholder="Search"
        value={searchQuery.value}
        onInput={(e) => {
          const q = (e.target as HTMLInputElement).value;
          void runSearch(q);
          // Typing switches to results and stops when the box is cleared, so
          // there is no separate "search mode" to leave.
          view.value = q.trim() ? "search" : "all";
        }}
      />

      <button
        class={`nav-item ${view.value === "all" ? "active" : ""}`}
        onClick={() => {
          view.value = "all";
          currentAlbum.value = null;
          clearSearch();
        }}
      >
        All photos
      </button>

      <div class="nav-section">
        <span>Albums</span>
        <button class="tiny" title="New album" onClick={() => setCreating(true)}>
          +
        </button>
      </div>

      {creating && (
        <form class="new-album" onSubmit={submit}>
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
      )}

      {albums.value.length === 0 && !creating && (
        <p class="nav-empty">No albums yet.</p>
      )}

      <button
        class={`nav-item ${view.value === "trash" ? "active" : ""}`}
        onClick={() => {
          view.value = "trash";
          currentAlbum.value = null;
          clearSearch();
        }}
      >
        Trash
      </button>

      {albums.value.map((a) => (
        <button
          key={a.id}
          class={`nav-item album ${
            view.value === "album" && currentAlbum.value?.id === a.id ? "active" : ""
          }`}
          onClick={() => {
            view.value = "album";
            clearSearch();
            void openAlbum(a.id);
          }}
          title={a.title}
        >
          <span class="label">{a.title}</span>
          <span class="badge">{a.item_count}</span>
        </button>
      ))}
    </nav>
  );
}
