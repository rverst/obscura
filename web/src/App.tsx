import { AlbumView } from "./AlbumView";
import { Login, RecoveryCodeNotice, Setup } from "./Auth";
import { Gallery } from "./Gallery";
import { Lightbox } from "./Lightbox";
import { SearchView } from "./SearchView";
import { collapsed, Sidebar, toggleSidebar } from "./Sidebar";
import { TrashView } from "./TrashView";
import { items, logout, recoveryCode, screen, username, view } from "./state";
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
          <header class="topbar">
            <button
              class="toggle"
              title={collapsed.value ? "Show sidebar" : "Hide sidebar"}
              aria-label={collapsed.value ? "Show sidebar" : "Hide sidebar"}
              onClick={toggleSidebar}
            >
              {collapsed.value ? "\u203A\u203A" : "\u2039\u2039"}
            </button>
            <h1>obscura</h1>
            {view.value === "all" && (
              <span class="count">
                {items.value.length} {items.value.length === 1 ? "photo" : "photos"}
              </span>
            )}
            <span class="spacer" />
            <span class="count">{username.value}</span>
            <button onClick={() => void logout()}>Lock</button>
          </header>
          <div class="body">
            <Sidebar />
            <main class="main">
              {view.value === "album" ? (
                <AlbumView />
              ) : view.value === "trash" ? (
                <TrashView />
              ) : view.value === "search" ? (
                <SearchView />
              ) : (
                <Gallery />
              )}
            </main>
          </div>
          {lightbox.value && <Lightbox media={lightbox.value} />}
        </div>
      );
  }
}
