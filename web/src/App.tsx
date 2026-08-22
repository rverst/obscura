import { AlbumView } from "./AlbumView";
import { Login, RecoveryCodeNotice, Setup } from "./Auth";
import { Gallery } from "./Gallery";
import { Lightbox } from "./Lightbox";
import { SearchView } from "./SearchView";
import { TrashView } from "./TrashView";
import { UnlockMethods } from "./UnlockMethods";
import { UserMenu } from "./UserMenu";
import { openLibraryRoot } from "./albums";
import { favoritesOnly, setFavoritesOnly } from "./favorites";
import { securityOpen } from "./security";
import { items, recoveryCode, screen, view } from "./state";
import { clearSearch, runSearch, searchQuery } from "./trash";
import { lightbox } from "./viewer";

export function App() {
  // The recovery code takes precedence over everything: it is shown once and
  // must not be lost behind another screen.
  if (recoveryCode.value) return <RecoveryCodeNotice />;

  switch (screen.value) {
    case "loading":
      return <p class="status">…</p>;
    case "setup":
      return <Setup />;
    case "login":
      return <Login />;
    case "gallery":
      return (
        <div class="app">
          <TopBar />
          <div class="body">
            <main class="main">
              {view.value === "trash" ? (
                <TrashView />
              ) : view.value === "search" ? (
                <SearchView />
              ) : view.value === "all" ? (
                <Gallery />
              ) : (
                <AlbumView />
              )}
            </main>
          </div>
          {securityOpen.value && <UnlockMethods />}
          {lightbox.value && <Lightbox media={lightbox.value} />}
        </div>
      );
  }
}

/**
 * The only chrome there is.
 *
 * Search sits here rather than in a sidebar because it applies to the whole
 * library wherever you are, and the sidebar it used to live in is gone: album
 * navigation moved into the content area as tiles and a breadcrumb, which is
 * the same place the nesting is.
 */
function TopBar() {
  return (
    <header class="topbar">
      <button
        class="brand"
        title="Back to the library"
        onClick={() => {
          view.value = "album";
          openLibraryRoot();
          clearSearch();
        }}
      >
        obscura
      </button>

      <input
        class="search"
        type="search"
        placeholder="Search"
        value={searchQuery.value}
        onInput={(e) => {
          const q = (e.target as HTMLInputElement).value;
          void runSearch(q);
          // Typing switches to results and clearing the box goes back to the
          // albums, so there is no separate "search mode" to leave.
          view.value = q.trim() ? "search" : "album";
        }}
      />

      {view.value === "all" && (
        <span class="count">
          {items.value.length} {items.value.length === 1 ? "photo" : "photos"}
        </span>
      )}

      <span class="spacer" />

      {/* One filter for the whole app rather than per view: turning it on and
          then navigating somewhere that ignored it would look like it had
          switched itself off. */}
      <button
        class={`fav-filter ${favoritesOnly.value ? "on" : ""}`}
        aria-pressed={favoritesOnly.value ? "true" : "false"}
        title={favoritesOnly.value ? "Showing favourites only" : "Show favourites only"}
        onClick={() => void setFavoritesOnly(!favoritesOnly.value)}
      >
        {favoritesOnly.value ? "♥" : "♡"}
      </button>

      <UserMenu />
    </header>
  );
}
