import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Media } from "./api";
import { items } from "./state";
import { enqueue, tasks } from "./upload";

function media(id: number, overrides: Partial<Media> = {}): Media {
  return {
    id,
    kind: "photo",
    filename: `p${id}.jpg`,
    mime: "image/jpeg",
    bytes: 100,
    sha256: "",
    width: 100,
    height: 100,
    created_at: 1000,
    has_thumb: true,
    ...overrides,
  };
}

/**
 * A scripted server.
 *
 * Records every request so the tests can assert on what the client actually did
 * -- which offsets it sent, whether it asked for the server's offset after a
 * failure -- rather than only on the final outcome.
 */
class FakeServer {
  received = 0;
  calls: { method: string; path: string; offset?: number }[] = [];
  /** Offsets at which the next PATCH should fail, consumed as they are hit. */
  failAt = new Set<number>();
  /** Simulates a chunk that arrived but whose response was lost. */
  silentlyAcceptAt = new Set<number>();
  finished = false;
  duplicate = false;
  existing: Media | null = null;

  handler = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const path = String(input);
    const method = init?.method ?? "GET";

    if (path === "/api/media/check") {
      this.calls.push({ method, path });
      return json({
        exists: this.existing !== null,
        media: this.existing ?? undefined,
      });
    }

    if (path === "/api/uploads" && method === "POST") {
      this.calls.push({ method, path });
      return json({ upload_id: "u1", offset: 0 }, 201);
    }

    if (path.endsWith("/finish")) {
      this.calls.push({ method, path });
      this.finished = true;
      return json({ duplicate: this.duplicate, media: media(42) }, 201);
    }

    if (method === "PATCH") {
      const offset = Number(headerOf(init, "Upload-Offset"));
      const size = blobSize(init?.body);
      this.calls.push({ method, path, offset });

      if (this.silentlyAcceptAt.has(offset)) {
        this.silentlyAcceptAt.delete(offset);
        this.received = offset + size; // it did arrive
        throw new TypeError("network error"); // the response did not
      }
      if (this.failAt.has(offset)) {
        this.failAt.delete(offset);
        throw new TypeError("network error");
      }
      if (offset > this.received) {
        return json({ error: "offset does not match" }, 409);
      }
      this.received = Math.max(this.received, offset + size);
      return json({ offset: this.received });
    }

    if (method === "GET") {
      this.calls.push({ method, path });
      return json({ upload_id: "u1", filename: "x", offset: this.received });
    }

    return json({ error: "unexpected" }, 500);
  };

  patchOffsets(): number[] {
    return this.calls.filter((c) => c.method === "PATCH").map((c) => c.offset!);
  }
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function headerOf(init: RequestInit | undefined, name: string): string {
  const h = init?.headers as Record<string, string> | undefined;
  return h?.[name] ?? "0";
}

function blobSize(body: BodyInit | null | undefined): number {
  if (body instanceof Blob) return body.size;
  return 0;
}

function fileOf(name: string, size: number): File {
  return new File([new Uint8Array(size)], name, { type: "image/jpeg" });
}

/** Waits until every queued task has reached a terminal state. */
async function settle(): Promise<void> {
  for (let i = 0; i < 400; i++) {
    const pending = tasks.value.some((t) =>
      ["queued", "hashing", "uploading", "processing"].includes(t.status),
    );
    if (!pending && tasks.value.length > 0) return;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error("uploads did not settle");
}

let server: FakeServer;

beforeEach(() => {
  server = new FakeServer();
  vi.stubGlobal("fetch", server.handler);
  tasks.value = [];
  items.value = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("upload", () => {
  it("uploads a file in chunks and reports it as added", async () => {
    enqueue([fileOf("holiday.jpg", 10 * 1024 * 1024)]);
    await settle();

    expect(tasks.value[0]!.status).toBe("done");
    expect(server.finished).toBe(true);
    expect(server.received).toBe(10 * 1024 * 1024);
    // 4 MB chunks over 10 MB is three requests.
    expect(server.patchOffsets()).toEqual([0, 4194304, 8388608]);
  });

  it("skips a file the server already has, without uploading it", async () => {
    server.existing = media(7);
    enqueue([fileOf("known.jpg", 1024)]);
    await settle();

    expect(tasks.value[0]!.status).toBe("duplicate");
    expect(server.patchOffsets()).toHaveLength(0);
    expect(server.finished).toBe(false);
  });

  // The point of the resumable protocol. A dropped connection must not restart
  // a multi-gigabyte upload from zero.
  it("resumes from the server's offset instead of restarting", async () => {
    server.failAt.add(4194304); // second chunk fails outright
    enqueue([fileOf("video.mp4", 12 * 1024 * 1024)]);
    await settle();

    expect(tasks.value[0]!.status).toBe("done");
    expect(server.received).toBe(12 * 1024 * 1024);

    // It must have asked what the server holds rather than guessing.
    expect(server.calls.some((c) => c.method === "GET")).toBe(true);
    // And it must never have gone back to the beginning.
    const offsets = server.patchOffsets();
    expect(offsets.filter((o) => o === 0)).toHaveLength(1);
  });

  // A chunk can arrive while its response is lost. Re-sending from the stale
  // offset would be refused as an overlap; the client has to notice the server
  // is further ahead than it thinks.
  it("recovers when a chunk arrived but its response did not", async () => {
    server.silentlyAcceptAt.add(0);
    enqueue([fileOf("photo.jpg", 8 * 1024 * 1024)]);
    await settle();

    expect(tasks.value[0]!.status).toBe("done");
    expect(server.received).toBe(8 * 1024 * 1024);
    // After recovery it continued at 4 MB, not at 0.
    const offsets = server.patchOffsets();
    expect(offsets[offsets.length - 1]).toBe(4194304);
  });

  it("gives up after repeated failures and reports the file as failed", async () => {
    // Every PATCH fails, however often it is retried.
    const original = server.handler;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      if ((init?.method ?? "GET") === "PATCH") throw new TypeError("network error");
      return original(input, init);
    });

    enqueue([fileOf("doomed.jpg", 1024)]);
    await settle();

    expect(tasks.value[0]!.status).toBe("error");
    expect(server.finished).toBe(false);
  });

  it("processes a batch sequentially so a Pi is not flooded", async () => {
    enqueue([fileOf("a.jpg", 1024), fileOf("b.jpg", 1024), fileOf("c.jpg", 1024)]);
    await settle();

    expect(tasks.value).toHaveLength(3);
    expect(tasks.value.every((t) => t.status === "done")).toBe(true);

    // Each file's create must be followed by its own finish before the next
    // create; interleaving would mean they ran concurrently.
    const seq = server.calls
      .filter((c) => c.path === "/api/uploads" || c.path.endsWith("/finish"))
      .map((c) => (c.path.endsWith("/finish") ? "finish" : "create"));
    expect(seq).toEqual(["create", "finish", "create", "finish", "create", "finish"]);
  });

  it("adds the finished item to the gallery", async () => {
    enqueue([fileOf("new.jpg", 1024)]);
    await settle();
    expect(items.value.map((m) => m.id)).toContain(42);
  });

  it("reports an empty selection as nothing to do", async () => {
    enqueue([]);
    expect(tasks.value).toHaveLength(0);
  });
});
