import { AlbumView, EditToggle } from "./AlbumView";
import { Login, RecoveryCodeNotice, Setup } from "./Auth";
import { Gallery } from "./Gallery";
import { Lightbox } from "./Lightbox";
import { SearchView } from "./SearchView";
import { TrashView } from "./TrashView";
import { UnlockMethods } from "./UnlockMethods";
import { UserMenu } from "./UserMenu";
import { favoritesOnly, setFavoritesOnly } from "./favorites";
import { navigate, navigateSearch, ROOT } from "./router";
import { securityOpen } from "./security";
import { items, recoveryCode, screen, view } from "./state";
import { searchQuery } from "./trash";
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
        onClick={() => navigate(ROOT)}
      >
        obscura
      </button>

      <input
        class="search"
        type="search"
        placeholder="Search"
        value={searchQuery.value}
        // Typing switches to results and clearing the box goes back to the
        // albums, so there is no separate "search mode" to leave.
        onInput={(e) => navigateSearch((e.target as HTMLInputElement).value)}
      />

      {view.value === "all" && (
        <>
          <span class="count">
            {items.value.length} {items.value.length === 1 ? "photo" : "photos"}
          </span>
          {/* The timeline has no header of its own, so its Edit lives here.
              Without it a photo that is in no album could not be deleted at
              all, now that the lightbox no longer offers it. */}
          <EditToggle />
        </>
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
