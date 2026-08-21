// Client side of the resumable upload protocol.
//
// The server can already resume an interrupted upload; this is the half that
// makes that worth anything. When a chunk fails, the client asks the server how
// much it actually holds and continues from there, instead of starting a
// multi-gigabyte video over because the wifi dropped for a second.

import { signal } from "@preact/signals";
import { ApiError, type Media } from "./api";
import { insertMedia } from "./state";

export type TaskStatus =
  | "queued"
  | "hashing"
  | "uploading"
  | "processing"
  | "done"
  | "duplicate"
  | "error";

export interface UploadTask {
  id: string;
  name: string;
  size: number;
  status: TaskStatus;
  /** 0..1, covering the byte transfer only. */
  progress: number;
  error?: string;
  media?: Media;
}

export const tasks = signal<UploadTask[]>([]);
export const uploading = signal(false);

/** Bytes per request. Small enough that a failure costs little, large enough
 *  that per-request overhead stays irrelevant on a slow link. */
const CHUNK_SIZE = 4 * 1024 * 1024;

/**
 * Above this, the duplicate pre-check is skipped.
 *
 * WebCrypto has no streaming digest, so hashing means holding the whole file in
 * memory at once. That is fine for photos and fatal for a 4 GB video. Large
 * files are simply uploaded; the server detects duplicates at the end anyway,
 * so the only cost is bandwidth that was going to be spent regardless.
 */
const HASH_LIMIT = 256 * 1024 * 1024;

/** How many times a single chunk is retried before the task fails. */
const MAX_RETRIES = 4;

let queue: UploadTask[] = [];
let files = new Map<string, File>();
let running = false;

export function enqueue(selected: File[]): void {
  const added: UploadTask[] = [];
  for (const file of selected) {
    const id = `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
    files.set(id, file);
    added.push({
      id,
      name: file.name,
      size: file.size,
      status: "queued",
      progress: 0,
    });
  }
  if (added.length === 0) return;

  tasks.value = [...tasks.value, ...added];
  queue.push(...added);
  void drain();
}

/** Removes finished entries from the list. */
export function clearFinished(): void {
  const keep = new Set(["queued", "hashing", "uploading", "processing"]);
  tasks.value = tasks.value.filter((t) => keep.has(t.status));
}

function update(id: string, patch: Partial<UploadTask>): void {
  tasks.value = tasks.value.map((t) => (t.id === id ? { ...t, ...patch } : t));
}

/**
 * Processes the queue one file at a time.
 *
 * Sequential on purpose: the server processes two files concurrently and runs
 * on a Pi, so flooding it with parallel uploads would not finish the batch any
 * sooner and would make each individual file take longer to appear.
 */
async function drain(): Promise<void> {
  if (running) return;
  running = true;
  uploading.value = true;

  while (queue.length > 0) {
    const task = queue.shift()!;
    const file = files.get(task.id);
    if (!file) continue;
    try {
      await uploadOne(task, file);
    } catch (err) {
      update(task.id, { status: "error", error: messageOf(err) });
    } finally {
      files.delete(task.id);
    }
  }

  running = false;
  uploading.value = false;
}

async function uploadOne(task: UploadTask, file: File): Promise<void> {
  // Ask before sending anything. Re-adding a folder is then nearly free.
  if (file.size <= HASH_LIMIT) {
    update(task.id, { status: "hashing" });
    const sum = await sha256Hex(file);
    const found = await checkHash(sum);
    if (found) {
      update(task.id, { status: "duplicate", progress: 1, media: found });
      return;
    }
  }

  update(task.id, { status: "uploading", progress: 0 });
  const uploadID = await createUpload(file);

  let offset = 0;
  let attempts = 0;

  while (offset < file.size) {
    const end = Math.min(offset + CHUNK_SIZE, file.size);
    try {
      offset = await sendChunk(uploadID, file.slice(offset, end), offset);
      attempts = 0;
      update(task.id, { progress: file.size > 0 ? offset / file.size : 1 });
    } catch (err) {
      attempts++;
      if (attempts > MAX_RETRIES) throw err;

      // This is the point of the whole protocol: rather than restarting, ask
      // what the server actually holds and continue from exactly there. A
      // chunk whose response was lost has in fact arrived, and re-sending it
      // from a stale offset would be refused as a gap.
      const known = await serverOffset(uploadID);
      if (known === null) throw err;
      offset = known;
      update(task.id, { progress: file.size > 0 ? offset / file.size : 1 });
      await delay(400 * attempts);
    }
  }

  // Deriving thumbnails takes noticeable time for a 24 MP frame, so the task
  // gets its own state rather than appearing stuck at 100%.
  update(task.id, { status: "processing", progress: 1 });
  const result = await finishUpload(uploadID);

  if (result.duplicate) {
    update(task.id, { status: "duplicate", media: result.media });
  } else {
    update(task.id, { status: "done", media: result.media });
  }
  insertMedia(result.media);
}

// --- protocol pieces --------------------------------------------------------

async function createUpload(file: File): Promise<string> {
  const resp = await fetch("/api/uploads", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ filename: file.name, size: file.size }),
  });
  const body = await readJSON(resp);
  return (body as { upload_id: string }).upload_id;
}

/** Sends one chunk and returns the server's new offset. */
async function sendChunk(uploadID: string, blob: Blob, offset: number): Promise<number> {
  const resp = await fetch(`/api/uploads/${uploadID}`, {
    method: "PATCH",
    headers: { "Upload-Offset": String(offset) },
    credentials: "same-origin",
    body: blob,
  });
  const body = await readJSON(resp);
  return (body as { offset: number }).offset;
}

async function serverOffset(uploadID: string): Promise<number | null> {
  try {
    const resp = await fetch(`/api/uploads/${uploadID}`, { credentials: "same-origin" });
    const body = await readJSON(resp);
    return (body as { offset: number }).offset;
  } catch {
    // The upload is gone -- swept, aborted, or the server restarted. Nothing
    // to resume onto.
    return null;
  }
}

async function finishUpload(
  uploadID: string,
): Promise<{ duplicate: boolean; media: Media }> {
  const resp = await fetch(`/api/uploads/${uploadID}/finish`, {
    method: "POST",
    credentials: "same-origin",
  });
  return (await readJSON(resp)) as { duplicate: boolean; media: Media };
}

async function checkHash(sha256: string): Promise<Media | null> {
  const resp = await fetch("/api/media/check", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ sha256 }),
  });
  const body = (await readJSON(resp)) as { exists: boolean; media?: Media };
  return body.exists && body.media ? body.media : null;
}

async function readJSON(resp: Response): Promise<unknown> {
  let body: unknown = null;
  try {
    body = await resp.json();
  } catch {
    body = null;
  }
  if (!resp.ok) {
    const message =
      (body as { error?: string } | null)?.error ?? `request failed (${resp.status})`;
    throw new ApiError(resp.status, message);
  }
  return body;
}

// --- helpers ----------------------------------------------------------------

async function sha256Hex(file: File): Promise<string> {
  const buf = await file.arrayBuffer();
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "upload failed";
}
