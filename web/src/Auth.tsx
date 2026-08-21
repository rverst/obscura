import { useState } from "preact/hooks";
import { authError, login, loginWithRecovery, recoveryCode, screen, setup } from "./state";

/** First-run screen. Creates the one account and shows the recovery code once. */
export function Setup() {
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    await setup(user, password);
    setBusy(false);
  };

  return (
    <div class="auth">
      <form onSubmit={submit}>
        <h2>Set up obscura</h2>
        <p class="hint">
          Your password derives the key that encrypts the library. The server never
          stores it.
        </p>
        {authError.value && <p class="error">{authError.value}</p>}
        <label>
          Username
          <input
            value={user}
            onInput={(e) => setUser((e.target as HTMLInputElement).value)}
            autocomplete="username"
            required
          />
        </label>
        <label>
          Password
          <input
            type="password"
            value={password}
            onInput={(e) => setPassword((e.target as HTMLInputElement).value)}
            autocomplete="new-password"
            minLength={8}
            required
          />
        </label>
        <button class="primary" type="submit" disabled={busy}>
          {busy ? "Setting up…" : "Create library"}
        </button>
      </form>
    </div>
  );
}

export function Login() {
  const [user, setUser] = useState("");
  const [secret, setSecret] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    if (useRecovery) await loginWithRecovery(user, secret);
    else await login(user, secret);
    setBusy(false);
  };

  return (
    <div class="auth">
      <form onSubmit={submit}>
        <h2>obscura</h2>
        <p class="hint">
          {useRecovery
            ? "Enter your recovery code. Spacing and case do not matter."
            : "Unlock the library."}
        </p>
        {authError.value && <p class="error">{authError.value}</p>}
        <label>
          Username
          <input
            value={user}
            onInput={(e) => setUser((e.target as HTMLInputElement).value)}
            autocomplete="username"
            required
          />
        </label>
        <label>
          {useRecovery ? "Recovery code" : "Password"}
          <input
            type={useRecovery ? "text" : "password"}
            value={secret}
            onInput={(e) => setSecret((e.target as HTMLInputElement).value)}
            autocomplete={useRecovery ? "off" : "current-password"}
            required
          />
        </label>
        <button class="primary" type="submit" disabled={busy}>
          {busy ? "Unlocking…" : "Unlock"}
        </button>
        <p class="switch">
          <button
            type="button"
            onClick={() => {
              setUseRecovery(!useRecovery);
              setSecret("");
              authError.value = "";
            }}
          >
            {useRecovery ? "Use password instead" : "Use recovery code"}
          </button>
        </p>
      </form>
    </div>
  );
}

/**
 * Shown once, immediately after setup. The code exists nowhere else -- not on
 * the server, not in storage here -- so this is genuinely the only chance to
 * write it down.
 */
export function RecoveryCodeNotice() {
  return (
    <div class="auth">
      <div class="panel">
        <h2>Write this down</h2>
        <p class="hint">Your recovery code:</p>
        <div class="recovery-code">{recoveryCode.value}</div>
        <p class="warn">
          This is shown once and is stored nowhere. If you lose both your password
          and this code, the library cannot be recovered — there is no back door.
          Keep it somewhere other than the server.
        </p>
        <button
          class="primary"
          onClick={() => {
            recoveryCode.value = null;
            screen.value = "gallery";
          }}
        >
          I have saved it
        </button>
      </div>
    </div>
  );
}
