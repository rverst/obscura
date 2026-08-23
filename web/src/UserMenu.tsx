import { useEffect, useRef, useState } from "preact/hooks";
import { navigate } from "./router";
import { openSecurity } from "./security";
import { logout, username } from "./state";

/**
 * Everything about the account and the library's plumbing, behind one control.
 *
 * The bar used to carry a button per destination, which does not survive
 * adding a third or a fourth. The trash lives here too: it is somewhere you go
 * occasionally and deliberately, not a place to keep one click away from a
 * photograph.
 */
export function UserMenu() {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  // Click-away and Escape, because a menu that can only be closed by the
  // control that opened it is a trap on a phone.
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", away);
    window.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", away);
      window.removeEventListener("keydown", key);
    };
  }, [open]);

  const go = (to: "trash") => {
    navigate({ view: to, photo: null });
    setOpen(false);
  };

  return (
    <div class="user-menu" ref={box}>
      <button
        class="user-button"
        aria-haspopup="menu"
        aria-expanded={open ? "true" : "false"}
        onClick={() => setOpen(!open)}
        title={username.value}
      >
        {username.value || "Account"}
        <span class="caret" aria-hidden="true">
          ▾
        </span>
      </button>

      {open && (
        <div class="menu" role="menu">
          <button role="menuitem" onClick={() => go("trash")}>
            Trash
          </button>
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false);
              void openSecurity();
            }}
          >
            Unlock methods
          </button>
          <hr />
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false);
              void logout();
            }}
          >
            Lock library
          </button>
        </div>
      )}
    </div>
  );
}
