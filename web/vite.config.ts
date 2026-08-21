import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

export default defineConfig({
  plugins: [preact()],
  build: {
    // The Go binary embeds this directory, so it has to be self-contained.
    outDir: "dist",
    // Not emptied wholesale: dist/.gitkeep is committed so that go:embed works
    // in a clone without a Node toolchain, and emptying would delete it. The
    // build script removes dist/assets instead, which is where the hashed
    // filenames would otherwise accumulate.
    emptyOutDir: false,
    // Source maps would ship the whole frontend source inside the image for no
    // benefit on a Pi.
    sourcemap: false,
  },
  server: {
    // `npm run dev` talks to a locally running obscurad. Same origin, so the
    // session cookie behaves exactly as it does in production.
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
    },
  },
});
