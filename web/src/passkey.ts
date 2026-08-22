// Passkey ceremonies.
//
// A passkey here is not a second factor. It is a third way to unwrap the key
// that decrypts the library, alongside the password and the recovery code, and
// that is only possible because of the PRF extension: an ordinary passkey
// signature differs every time and is useless as key material, while PRF
// returns the same 32 bytes for the same salt on every use.
//
// The raw PRF output never leaves this file. What goes to the server is an
// HKDF of it, and only ever over TLS.

import { api } from "./api";

/**
 * HKDF info string. Must match auth.PRFInfo on the server, byte for byte.
 *
 * It is not a secret. It exists so the same PRF output cannot be reused as a
 * key for something else -- and because a mismatch here produces a passkey that
 * enrolls happily and then never unlocks anything.
 */
const PRF_INFO = "obscura-unlock-v1";

/**
 * Thrown when the device can sign in but cannot produce key material.
 *
 * Distinct from a general failure because it is the one case with a real
 * answer for the user: use the password on this device. PRF support is still
 * patchy -- it depends on the browser, the platform and the authenticator, and
 * a security key plugged into the wrong platform can lose it.
 */
export class PasskeyUnsupported extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PasskeyUnsupported";
  }
}

/** Whether this browser could do passkeys at all. */
export function passkeysAvailable(): boolean {
  return (
    typeof window !== "undefined" &&
    window.isSecureContext === true &&
    typeof window.PublicKeyCredential === "function" &&
    typeof navigator.credentials?.create === "function"
  );
}

// --- Registration -----------------------------------------------------------

/**
 * Enrolls a passkey for the signed-in user.
 *
 * Nothing is stored server-side until a PRF result is in hand. A credential
 * that authenticates but cannot decrypt would be worse than no passkey at all:
 * it would look like a working way in and then fail at the point where the user
 * has already dismissed the password field.
 */
export async function enrollPasskey(label: string): Promise<void> {
  if (!passkeysAvailable()) {
    throw new PasskeyUnsupported("This browser cannot use passkeys.");
  }
  const begin = await api.passkeyRegisterBegin();
  const salt = fromBase64url(begin.prf_salt);

  const publicKey: PublicKeyCredentialCreationOptions = {
    ...decodeCreationOptions(begin.options.publicKey),
    // Asking for the result immediately. Browsers that support it answer here
    // and save the second prompt below.
    extensions: prfExtension({ eval: { first: salt } }),
  };

  let credential: PublicKeyCredential;
  try {
    credential = (await navigator.credentials.create({
      publicKey,
    })) as PublicKeyCredential;
  } catch (err) {
    await api.passkeyRegisterAbort(begin.ceremony).catch(() => undefined);
    throw err;
  }

  let secret: ArrayBuffer;
  try {
    secret = await prfResult(credential, salt);
  } catch (err) {
    // Drop the challenge rather than leaving it to expire, so no salt sits on
    // the server for a credential that is being thrown away.
    await api.passkeyRegisterAbort(begin.ceremony).catch(() => undefined);
    throw err;
  }

  await api.passkeyRegisterFinish({
    ceremony: begin.ceremony,
    label,
    unlock_key: await deriveUnlockKey(secret),
    credential: encodeAttestation(credential),
  });
}

/**
 * Gets the PRF output for a freshly created credential.
 *
 * Two shapes exist in the wild. Some browsers return the result straight from
 * create(); others only report that PRF is now enabled and require a second
 * ceremony to actually evaluate it. Both are handled, and a device that does
 * neither is told apart from one that failed for another reason.
 */
async function prfResult(
  credential: PublicKeyCredential,
  salt: Uint8Array<ArrayBuffer>,
): Promise<ArrayBuffer> {
  const results = extensionPRF(credential);
  if (results?.results?.first) return results.results.first;

  if (results?.enabled !== true) {
    throw new PasskeyUnsupported(
      "This device cannot produce an unlock key, so a passkey here could sign " +
        "in but not open the library. Use your password on this device.",
    );
  }

  // PRF is on but unevaluated: ask again, this time as an assertion. The
  // challenge is generated here and never sent anywhere -- this ceremony exists
  // only to make the authenticator compute the PRF, and its signature is
  // discarded.
  const assertion = (await navigator.credentials.get({
    publicKey: {
      challenge: randomChallenge(),
      allowCredentials: [{ id: credential.rawId, type: "public-key" }],
      userVerification: "required",
      extensions: prfExtension({ eval: { first: salt } }),
    },
  })) as PublicKeyCredential | null;

  const secret = assertion && extensionPRF(assertion)?.results?.first;
  if (!secret) {
    throw new PasskeyUnsupported(
      "This device reported an unlock key and then did not provide one. " +
        "Use your password here.",
    );
  }
  return secret;
}

// --- Sign-in ----------------------------------------------------------------

/** Signs in with a passkey and returns the username. */
export async function signInWithPasskey(username?: string): Promise<string> {
  if (!passkeysAvailable()) {
    throw new PasskeyUnsupported("This browser cannot use passkeys.");
  }
  const begin = await api.passkeyLoginBegin(username);

  // One salt per credential: the browser decides which authenticator to use
  // only once the user picks, so every candidate arrives with its own.
  const evalByCredential: Record<string, { first: BufferSource }> = {};
  for (const [id, salt] of Object.entries(begin.prf_salts)) {
    evalByCredential[id] = { first: fromBase64url(salt) };
  }

  const assertion = (await navigator.credentials.get({
    publicKey: {
      ...decodeRequestOptions(begin.options.publicKey),
      extensions: prfExtension({ evalByCredential }),
    },
  })) as PublicKeyCredential | null;

  if (!assertion) throw new Error("no passkey was chosen");
  const secret = extensionPRF(assertion)?.results?.first;
  if (!secret) {
    throw new PasskeyUnsupported(
      "This device did not return an unlock key. Use your password here.",
    );
  }

  const res = await api.passkeyLoginFinish({
    ceremony: begin.ceremony,
    unlock_key: await deriveUnlockKey(secret),
    credential: encodeAssertion(assertion),
  });
  return res.username;
}

// --- Key derivation ---------------------------------------------------------

/**
 * Turns the PRF output into the key the slot is wrapped with.
 *
 * Exported for the test that pins it against the Go side: this is a contract
 * between two languages, and a silent disagreement here would only show up as
 * a passkey that never unlocks.
 *
 * Extract-and-expand with an empty salt, which is what deriveBits does and what
 * the server's test authenticator mirrors. The PRF output is already uniform,
 * so nothing here is trying to stretch a weak secret.
 */
export async function deriveUnlockKey(secret: ArrayBuffer): Promise<string> {
  const key = await crypto.subtle.importKey("raw", secret, "HKDF", false, [
    "deriveBits",
  ]);
  const bits = await crypto.subtle.deriveBits(
    {
      name: "HKDF",
      hash: "SHA-256",
      salt: new Uint8Array(0),
      info: new TextEncoder().encode(PRF_INFO),
    },
    key,
    256,
  );
  return toBase64url(bits);
}

// --- Wire format ------------------------------------------------------------

// The server speaks base64url for every binary field. The browser wants
// ArrayBuffers. Converting by hand rather than relying on
// parseCreationOptionsFromJSON, which is not everywhere yet and whose handling
// of extension inputs is exactly the patchy part.

interface PRFResults {
  enabled?: boolean;
  results?: { first?: ArrayBuffer };
}

interface PRFInput {
  eval?: { first: BufferSource };
  evalByCredential?: Record<string, { first: BufferSource }>;
}

/**
 * The PRF extension is not in the DOM typings yet, so the cast is unavoidable.
 * Confined to one function rather than repeated at each call site, where it
 * would be easy to widen accidentally.
 */
function prfExtension(input: PRFInput): AuthenticationExtensionsClientInputs {
  return { prf: input } as unknown as AuthenticationExtensionsClientInputs;
}

/** Filled in place so the buffer type stays narrow enough for BufferSource. */
function randomChallenge(): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(new ArrayBuffer(32));
  crypto.getRandomValues(out);
  return out;
}

function extensionPRF(credential: PublicKeyCredential): PRFResults | undefined {
  return (credential.getClientExtensionResults() as { prf?: PRFResults }).prf;
}

export function fromBase64url(value: string): Uint8Array<ArrayBuffer> {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(padded.padEnd(Math.ceil(padded.length / 4) * 4, "="));
  const out = new Uint8Array(new ArrayBuffer(binary.length));
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

export function toBase64url(value: ArrayBuffer | Uint8Array): string {
  const bytes = value instanceof Uint8Array ? value : new Uint8Array(value);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

interface DescriptorJSON {
  id: string;
  type: string;
  transports?: string[];
}

function decodeDescriptors(list: DescriptorJSON[] | undefined) {
  return (list ?? []).map((c) => ({
    id: fromBase64url(c.id),
    type: c.type as "public-key",
    transports: c.transports as AuthenticatorTransport[] | undefined,
  }));
}

export function decodeCreationOptions(
  pk: Record<string, unknown>,
): PublicKeyCredentialCreationOptions {
  const user = pk.user as { id: string; name: string; displayName: string };
  return {
    ...(pk as unknown as PublicKeyCredentialCreationOptions),
    challenge: fromBase64url(pk.challenge as string),
    user: { ...user, id: fromBase64url(user.id) },
    excludeCredentials: decodeDescriptors(
      pk.excludeCredentials as DescriptorJSON[],
    ),
  };
}

export function decodeRequestOptions(
  pk: Record<string, unknown>,
): PublicKeyCredentialRequestOptions {
  return {
    ...(pk as unknown as PublicKeyCredentialRequestOptions),
    challenge: fromBase64url(pk.challenge as string),
    allowCredentials: decodeDescriptors(
      pk.allowCredentials as DescriptorJSON[],
    ),
  };
}

function encodeAttestation(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAttestationResponse;
  return {
    id: credential.id,
    rawId: toBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: {},
    response: {
      clientDataJSON: toBase64url(response.clientDataJSON),
      attestationObject: toBase64url(response.attestationObject),
      // Recorded at registration because it is the only moment it is offered,
      // and it is what lets a later sign-in hint at the right transport.
      transports: response.getTransports?.() ?? [],
    },
  };
}

function encodeAssertion(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAssertionResponse;
  return {
    id: credential.id,
    rawId: toBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: {},
    response: {
      clientDataJSON: toBase64url(response.clientDataJSON),
      authenticatorData: toBase64url(response.authenticatorData),
      signature: toBase64url(response.signature),
      userHandle: response.userHandle
        ? toBase64url(response.userHandle)
        : undefined,
    },
  };
}
