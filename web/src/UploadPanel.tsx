import { useCallback, useRef, useState } from "preact/hooks";
import { clearFinished, enqueue, tasks, uploading, type UploadTask } from "./upload";

/**
 * Scoped drop area, shown only while an album is being edited.
 *
 * Deliberately not a whole-window target. A window-wide drop zone competes with
 * dragging photos around to reorder them, and it cannot express which album an
 * upload belongs to. Here the target is explicit and so is the destination.
 */
export function UploadPanel({ albumID }: { albumID?: number }) {
  const [over, setOver] = useState(false);
  const picker = useRef<HTMLInputElement>(null);

  const carriesFiles = (e: DragEvent) =>
    Array.from(e.dataTransfer?.types ?? []).includes("Files");

  // The destination album is attached to each upload here, at the moment it is
  // queued. Filing uploads afterwards, based on whichever album was open by
  // then, put the first album's photos into the second one.
  const accept = useCallback(
    (files: File[]) => {
      if (files.length > 0) enqueue(files, albumID);
    },
    [albumID],
  );

  return (
    <section
      class={`upload-panel ${over ? "over" : ""}`}
      onDragEnter={(e) => carriesFiles(e) && setOver(true)}
      onDragOver={(e) => {
        // Without preventDefault the browser navigates to the dropped file and
        // the application disappears.
        if (carriesFiles(e)) e.preventDefault();
      }}
      onDragLeave={(e) => {
        // Only clear when the pointer actually left the panel, not merely
        // crossed onto a child element.
        if (!e.currentTarget || !(e.relatedTarget instanceof Node)) {
          setOver(false);
          return;
        }
        if (!(e.currentTarget as Node).contains(e.relatedTarget)) setOver(false);
      }}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        accept(Array.from(e.dataTransfer?.files ?? []));
      }}
    >
      <input
        ref={picker}
        type="file"
        multiple
        accept="image/*,video/*"
        style="display:none"
        onChange={(e) => {
          const input = e.target as HTMLInputElement;
          accept(Array.from(input.files ?? []));
          // Reset, or picking the same file twice in a row does nothing.
          input.value = "";
        }}
      />
      <p>
        Drop photos here, or{" "}
        <button class="link" onClick={() => picker.current?.click()}>
          choose files
        </button>
      </p>
      <UploadQueue albumID={albumID} />
    </section>
  );
}

function UploadQueue({ albumID }: { albumID?: number }) {
  // Only this album's uploads. The task list is global so a batch survives
  // navigation, but showing another album's files here would be confusing.
  const list = tasks.value.filter((t) => t.albumID === albumID);
  if (list.length === 0) return null;

  const finished = list.filter((t) => t.status === "done" || t.status === "duplicate").length;
  const failed = list.filter((t) => t.status === "error").length;

  return (
    <div class="upload-queue">
      <header>
        <strong>
          {uploading.value
            ? `Uploading ${Math.min(finished + 1, list.length)} of ${list.length}`
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
    </div>
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
