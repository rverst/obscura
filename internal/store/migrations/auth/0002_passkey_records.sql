-- Passkeys: store the WebAuthn credential record whole.
--
-- The original table split the record into columns (public_key, attestation,
-- transports, sign_count). That loses the parts a verifier needs but that are
-- easy to overlook: the AAGUID, and above all the backup-eligibility and
-- backup-state flags. go-webauthn checks those flags for consistency across
-- logins, so dropping them would surface much later as a passkey that
-- registers fine and then refuses to sign in.
--
-- The table has never held a row -- there was no passkey feature to write one --
-- so this replaces it rather than migrating anything.
DROP TABLE webauthn_credentials;

CREATE TABLE webauthn_credentials(
    credential_id BLOB PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- The complete credential record as JSON, rewritten after every successful
    -- login so the signature counter stays current. One source of truth beats
    -- a column that can drift away from the record beside it.
    record        TEXT NOT NULL,

    created_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_webauthn_credentials_user ON webauthn_credentials(user_id);

-- The WebAuthn user handle is stored on the authenticator and travels back in
-- every assertion. Random rather than the row id: a handle of 0x01 would tell
-- anyone holding the device that this is the first account on the server.
-- Filled in when the first passkey is registered.
ALTER TABLE users ADD COLUMN webauthn_handle BLOB;
