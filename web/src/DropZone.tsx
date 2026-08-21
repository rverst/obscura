import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { clearFinished, enqueue, tasks, uploading, type UploadTask } from "./upload";

/**
 * Whole-window drop target.
 *
 * The overlay only appears once a drag carrying files enters the window, so it
 * never sits in the way of looking at photographs -- which is what the app is
 * for the rest of the time.
 */
export function DropZone() {
  const [dragging, setDragging] = useState(false);
  const picker = useRef<HTMLInputElement>(null);

  // Drag events fire for every element entered and left, so a plain
  // enter/leave pair flickers constantly. Counting them is the standard fix.
  const depth = useRef(0);

  const carriesFiles = (e: DragEvent) =>
    Array.from(e.dataTransfer?.types ?? []).includes("Files");

  useEffect(() => {
    const onEnter = (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      depth.current++;
      setDragging(true);
    };
    const onLeave = (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setDragging(false);
    };
    const onOver = (e: DragEvent) => {
      // Without this the browser navigates to the dropped file and the app
      // disappears -- the single most common way a drop target does not work.
      if (carriesFiles(e)) e.preventDefault();
    };
    const onDrop = (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      depth.current = 0;
      setDragging(false);
      const files = Array.from(e.dataTransfer?.files ?? []);
      if (files.length > 0) enqueue(files);
    };

    window.addEventListener("dragenter", onEnter);
    window.addEventListener("dragleave", onLeave);
    window.addEventListener("dragover", onOver);
    window.addEventListener("drop", onDrop);
    return () => {
      window.removeEventListener("dragenter", onEnter);
      window.removeEventListener("dragleave", onLeave);
      window.removeEventListener("dragover", onOver);
      window.removeEventListener("drop", onDrop);
    };
  }, []);

  const onPick = useCallback((e: Event) => {
    const input = e.target as HTMLInputElement;
    const files = Array.from(input.files ?? []);
    if (files.length > 0) enqueue(files);
    // Reset, or picking the same file twice in a row does nothing.
    input.value = "";
  }, []);

  return (
    <>
      <input
        ref={picker}
        type="file"
        multiple
        accept="image/*,video/*"
        style="display:none"
        onChange={onPick}
      />
      <button onClick={() => picker.current?.click()}>Add photos</button>

      {dragging && (
        <div class="drop-overlay">
          <div class="drop-hint">Drop to upload</div>
        </div>
      )}

      <UploadQueue />
    </>
  );
}

function UploadQueue() {
  const list = tasks.value;
  if (list.length === 0) return null;

  const done = list.filter((t) => t.status === "done" || t.status === "duplicate").length;
  const failed = list.filter((t) => t.status === "error").length;

  return (
    <aside class="upload-queue">
      <header>
        <strong>
          {uploading.value
            ? `Uploading ${done + 1} of ${list.length}`
            : `${list.length} ${list.length === 1 ? "file" : "files"}`}
        </strong>
        {failed > 0 && <span class="failed">{failed} failed</span>}
        {!uploading.value && (
          <button class="dismiss" onClick={clearFinished}>
            Clear
          </button>
        )}
      </header>
      <ul>
        {list.map((t) => (
          <QueueRow key={t.id} task={t} />
        ))}
      </ul>
    </aside>
  );
}

function QueueRow({ task }: { task: UploadTask }) {
  return (
    <li class={`task ${task.status}`}>
      <div class="row">
        <span class="name" title={task.name}>
          {task.name}
        </span>
        <span class="state">{label(task)}</span>
      </div>
      {(task.status === "uploading" || task.status === "processing") && (
        <div class="bar">
          <div class="fill" style={{ width: `${Math.round(task.progress * 100)}%` }} />
        </div>
      )}
      {task.error && <div class="err">{task.error}</div>}
    </li>
  );
}

function label(t: UploadTask): string {
  switch (t.status) {
    case "queued":
      return "waiting";
    case "hashing":
      return "checking";
    case "uploading":
      return `${Math.round(t.progress * 100)}%`;
    case "processing":
      // Deriving thumbnails from a 24 MP frame is slow enough to need saying,
      // otherwise the bar sits at 100% and looks stuck.
      return "processing";
    case "done":
      return "added";
    case "duplicate":
      return "already here";
    case "error":
      return "failed";
  }
}
