import { useState } from "preact/hooks";
import type { UnlockMethod } from "./api";
import {
  addPasskey,
  canEnroll,
  closeSecurity,
  removePasskey,
  securityBusy,
  securityError,
  unlockMethods,
} from "./security";

const KIND_NAMES: Record<UnlockMethod["kind"], string> = {
  password: "Password",
  recovery: "Recovery code",
  passkey: "Passkey",
};

function formatDate(seconds: number): string {
  return new Date(seconds * 1000).toLocaleDateString();
}

/** Panel listing everything that can open the library. */
export function UnlockMethods() {
  const [label, setLabel] = useState("");

  const submit = async (e: Event) => {
    e.preventDefault();
    if (await addPasskey(label)) setLabel("");
  };

  const passkeys = unlockMethods.value.filter((m) => m.kind === "passkey");

  return (
    <div class="overlay" onClick={closeSecurity}>
      <div class="panel" onClick={(e) => e.stopPropagation()}>
        <h2>Unlocking this library</h2>
        <p class="hint">
          Each of these unwraps the same key. Adding or removing one never
          re-encrypts a photo.
        </p>

        {securityError.value && <p class="error">{securityError.value}</p>}

        <ul class="slots">
          {unlockMethods.value.map((m) => (
            <li key={m.id}>
              <span class="kind">{KIND_NAMES[m.kind] ?? m.kind}</span>
              {m.kind === "passkey" && <span class="label">{m.label}</span>}
              <span class="when">added {formatDate(m.created_at)}</span>
              {m.kind === "passkey" ? (
                <button
                  class="tiny danger"
                  onClick={() => void removePasskey(m.id)}
                >
                  Remove
                </button>
              ) : (
                // Deleting either of these is how someone locks themselves out
                // of their own library for good, so the button does not exist.
                <span class="fixed" title="Cannot be removed">
                  —
                </span>
              )}
            </li>
          ))}
        </ul>

        {canEnroll.value ? (
          <form class="add-passkey" onSubmit={submit}>
            <input
              value={label}
              placeholder={passkeys.length ? "Another device" : "This device"}
              onInput={(e) => setLabel((e.target as HTMLInputElement).value)}
            />
            <button class="primary" type="submit" disabled={securityBusy.value}>
              {securityBusy.value ? "Waiting for the device…" : "Add passkey"}
            </button>
          </form>
        ) : (
          <p class="warn">
            Passkeys need an https address (or localhost) and a browser that
            supports them. Not available here.
          </p>
        )}

        <p class="hint">
          A passkey only works if the device can derive a key from it. If yours
          cannot, you will be told during setup and nothing is saved — your
          password keeps working either way.
        </p>

        <button onClick={closeSecurity}>Close</button>
      </div>
    </div>
  );
}
