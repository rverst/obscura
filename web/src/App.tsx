import { Login, RecoveryCodeNotice, Setup } from "./Auth";
import { Gallery } from "./Gallery";
import { Lightbox } from "./Lightbox";
import { items, lightbox, logout, recoveryCode, screen, username } from "./state";

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
            <h1>obscura</h1>
            <span class="count">
              {items.value.length} {items.value.length === 1 ? "photo" : "photos"}
            </span>
            <span class="spacer" />
            <span class="count">{username.value}</span>
            <button onClick={() => void logout()}>Lock</button>
          </header>
          <Gallery />
          {lightbox.value && <Lightbox media={lightbox.value} />}
        </div>
      );
  }
}
