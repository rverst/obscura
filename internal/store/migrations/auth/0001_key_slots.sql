-- auth.db: deliberately UNENCRYPTED.
--
-- This file must be readable before any key exists -- otherwise login would be
-- a chicken-and-egg problem. It therefore contains only material that is
-- worthless without the corresponding secret: salts, KDF parameters and DEK
-- copies wrapped with a KEK.
--
-- Nothing that carries meaning without the password may EVER land here -- no
-- filenames, no album titles, no counts describing the library.

CREATE TABLE users(
    id         INTEGER PRIMARY KEY,
    username   TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
) STRICT;

-- A slot is one way to obtain the DEK, modelled on LUKS key slots. Several
-- slots unlock the same DEK.
--
-- That is precisely why a password change rewrites a single row here and leaves
-- every blob untouched.
CREATE TABLE key_slots(
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- 'password' | 'recovery' | 'passkey'
    kind         TEXT NOT NULL CHECK(kind IN ('password','recovery','passkey')),

    -- Human-readable name, e.g. "Ralf's iPhone" for passkeys.
    label        TEXT NOT NULL DEFAULT '',

    -- Argon2id parameters as JSON (salt, t, m, p). Stored per slot so the cost
    -- can be raised later without breaking existing slots.
    kdf_params   TEXT,

    -- The DEK sealed with this slot's KEK (AES-256-GCM, nonce prepended).
    wrapped_dek  BLOB NOT NULL,

    -- Only for kind='passkey': WebAuthn credential ID and the PRF salt.
    credential_id BLOB,
    prf_salt      BLOB,

    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
) STRICT;

CREATE INDEX idx_key_slots_user ON key_slots(user_id, kind);
CREATE UNIQUE INDEX idx_key_slots_credential
    ON key_slots(credential_id) WHERE credential_id IS NOT NULL;

-- WebAuthn needs a per-credential signature counter to detect cloned
-- authenticators. It lives here because it is needed before unlocking.
CREATE TABLE webauthn_credentials(
    credential_id BLOB PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    public_key    BLOB NOT NULL,
    attestation   TEXT NOT NULL DEFAULT '',
    transports    TEXT NOT NULL DEFAULT '',
    sign_count    INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
) STRICT;
