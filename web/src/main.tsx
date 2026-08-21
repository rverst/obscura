import { render } from "preact";
import { App } from "./App";
import { boot } from "./state";
import "./styles.css";

const root = document.getElementById("app");
if (!root) throw new Error("missing #app element");

render(<App />, root);
void boot();

// Registered after the app is up, so a failing worker can never keep the page
// from rendering. Service workers require a secure context, which localhost
// counts as -- in production that means the TLS proxy is doing its job.
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js").catch(() => {
      // An unavailable worker costs offline support and nothing else.
    });
  });
}
