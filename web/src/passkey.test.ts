import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  PasskeyUnsupported,
  decodeCreationOptions,
  decodeRequestOptions,
  deriveUnlockKey,
  dropEnrollment,
  dropSignIn,
  enrollPasskey,
  fromBase64url,
  prepareEnrollment,
  prepareSignIn,
  signInWithPasskey,
  toBase64url,
} from "./passkey";
import { api } from "./api";

vi.mock("./api", () => ({
  api: {
    passkeyRegisterBegin: vi.fn(),
    passkeyRegisterFinish: vi.fn(),
    passkeyRegisterAbort: vi.fn(),
    passkeyLoginBegin: vi.fn(),
    passkeyLoginFinish: vi.fn(),
  },
}));

type Mock = ReturnType<typeof vi.fn>;

const mocked = api as unknown as {
  passkeyRegisterBegin: Mock;
  passkeyRegisterFinish: Mock;
  passkeyRegisterAbort: Mock;
  passkeyLoginBegin: Mock;
  passkeyLoginFinish: Mock;
};

/** The arguments of a mock's first call, without index-signature noise. */
function firstCall(mock: Mock): any {
  return mock.mock.calls[0]![0];
}

/** A credential as the browser hands it back, with whatever PRF result we want. */
function fakeCredential(prf: unknown, kind: "create" | "get" = "create") {
  const response =
    kind === "create"
      ? {
          clientDataJSON: new Uint8Array([1, 2, 3]).buffer,
          attestationObject: new Uint8Array([4, 5, 6]).buffer,
          getTransports: () => ["internal"],
        }
      : {
          clientDataJSON: new Uint8Array([1, 2, 3]).buffer,
          authenticatorData: new Uint8Array([7, 8]).buffer,
          signature: new Uint8Array([9]).buffer,
          userHandle: new Uint8Array([10]).buffer,
        };
  return {
    id: "Y3JlZA",
    rawId: new Uint8Array([99, 114, 101, 100]).buffer,
    type: "public-key",
    authenticatorAttachment: "platform",
    response,
    getClientExtensionResults: () => ({ prf }),
  };
}

function stubBrowser(create: unknown, get: unknown) {
  vi.stubGlobal("window", {
    isSecureContext: true,
    PublicKeyCredential: function () {},
  });
  vi.stubGlobal("navigator", {
    credentials: {
      create: vi.fn().mockResolvedValue(create),
      get: vi.fn().mockResolvedValue(get),
    },
  });
}

const beginBody = {
  ceremony: "cer-1",
  prf_salt: toBase64url(new Uint8Array([1, 2, 3, 4])),
  options: {
    publicKey: {
      challenge: toBase64url(new Uint8Array([9, 9, 9])),
      rp: { id: "photos.example.com", name: "obscura" },
      user: {
        id: toBase64url(new Uint8Array([5, 5])),
        name: "ralf",
        displayName: "ralf",
      },
      pubKeyCredParams: [{ type: "public-key", alg: -7 }],
      excludeCredentials: [
        { id: toBase64url(new Uint8Array([7])), type: "public-key" },
      ],
    },
  },
};

beforeEach(() => {
  // The challenges kept ready live in the module, so they outlast a test.
  dropEnrollment();
  dropSignIn();
  vi.useRealTimers();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe("base64url", () => {
  it("round-trips every length, so no padding case is left untested", () => {
    for (let n = 0; n < 12; n++) {
      const bytes = new Uint8Array(n).map((_, i) => (i * 37) % 256);
      expect(Array.from(fromBase64url(toBase64url(bytes)))).toEqual(
        Array.from(bytes),
      );
    }
  });

  it("emits the url alphabet, because these values travel in JSON and URLs", () => {
    const encoded = toBase64url(new Uint8Array([251, 255, 190]));
    expect(encoded).not.toMatch(/[+/=]/);
    expect(Array.from(fromBase64url(encoded))).toEqual([251, 255, 190]);
  });
});

describe("option decoding", () => {
  it("turns every binary field into bytes and leaves the rest alone", () => {
    const options = decodeCreationOptions(beginBody.options.publicKey);
    expect(options.challenge).toBeInstanceOf(Uint8Array);
    expect(options.user.id).toBeInstanceOf(Uint8Array);
    expect(Array.from(options.user.id as Uint8Array)).toEqual([5, 5]);
    expect(options.excludeCredentials![0]!.id).toBeInstanceOf(Uint8Array);
    // Untouched fields have to survive: dropping pubKeyCredParams or rp would
    // fail inside the browser with a message that points nowhere useful.
    expect(options.rp.id).toBe("photos.example.com");
    expect(options.pubKeyCredParams).toHaveLength(1);
  });

  it("decodes the credentials offered at sign-in", () => {
    const options = decodeRequestOptions({
      challenge: toBase64url(new Uint8Array([1])),
      allowCredentials: [
        {
          id: toBase64url(new Uint8Array([2, 2])),
          type: "public-key",
          transports: ["usb"],
        },
      ],
    });
    expect(Array.from(options.allowCredentials![0]!.id as Uint8Array)).toEqual([
      2, 2,
    ]);
    expect(options.allowCredentials![0]!.transports).toEqual(["usb"]);
  });
});

// The one value that has to agree with the server across two languages and two
// HKDF implementations. Computed with Go's crypto/hkdf; if this ever drifts,
// every passkey stops unlocking and nothing else would say why.
describe("unlock key derivation", () => {
  it("matches the Go side byte for byte", async () => {
    const secret = new Uint8Array(32).map((_, i) => i);
    expect(await deriveUnlockKey(secret.buffer)).toBe(
      "B182WkcRRvkQRq9Ha3-lbx5j5zvaWIAiB9mM1cBrnIU",
    );
  });
});

describe("enrollment", () => {
  it("uses the PRF result when create() supplies one", async () => {
    stubBrowser(
      fakeCredential({ results: { first: new Uint8Array(32).buffer } }),
      null,
    );
    mocked.passkeyRegisterBegin.mockResolvedValue(beginBody);
    mocked.passkeyRegisterFinish.mockResolvedValue(undefined);

    await enrollPasskey("Phone");

    expect(navigator.credentials.get).not.toHaveBeenCalled();
    const sent = firstCall(mocked.passkeyRegisterFinish);
    expect(sent.ceremony).toBe("cer-1");
    expect(sent.label).toBe("Phone");
    expect(sent.unlock_key).toHaveLength(43); // 32 bytes, base64url, unpadded
    expect(sent.credential.response.attestationObject).toBeTruthy();
  });

  // Some browsers only report that PRF is now on and make you ask again.
  it("asks a second time when create() only reports PRF as enabled", async () => {
    const secret = new Uint8Array(32).fill(7).buffer;
    stubBrowser(
      fakeCredential({ enabled: true }),
      fakeCredential({ results: { first: secret } }, "get"),
    );
    mocked.passkeyRegisterBegin.mockResolvedValue(beginBody);
    mocked.passkeyRegisterFinish.mockResolvedValue(undefined);

    await enrollPasskey("Phone");

    expect(navigator.credentials.get).toHaveBeenCalledTimes(1);
    const sent = firstCall(mocked.passkeyRegisterFinish);
    expect(sent.unlock_key).toBe(await deriveUnlockKey(secret));
    // The second ceremony exists only to evaluate the PRF; it must be aimed at
    // the credential just created, not at whatever else is on the device.
    const asked = firstCall(navigator.credentials.get as Mock);
    expect(asked.publicKey.allowCredentials).toHaveLength(1);
    expect(asked.publicKey.userVerification).toBe("required");
  });

  // The case the whole design has to survive: a device that authenticates but
  // cannot produce key material. Nothing may be registered.
  it("registers nothing when the device cannot do PRF", async () => {
    stubBrowser(fakeCredential(undefined), null);
    mocked.passkeyRegisterBegin.mockResolvedValue(beginBody);
    mocked.passkeyRegisterAbort.mockResolvedValue(undefined);

    await expect(enrollPasskey("Phone")).rejects.toBeInstanceOf(
      PasskeyUnsupported,
    );

    expect(mocked.passkeyRegisterFinish).not.toHaveBeenCalled();
    expect(mocked.passkeyRegisterAbort).toHaveBeenCalledWith("cer-1");
  });

  it("registers nothing when the second ceremony comes back empty", async () => {
    stubBrowser(
      fakeCredential({ enabled: true }),
      fakeCredential(undefined, "get"),
    );
    mocked.passkeyRegisterBegin.mockResolvedValue(beginBody);
    mocked.passkeyRegisterAbort.mockResolvedValue(undefined);

    await expect(enrollPasskey("Phone")).rejects.toBeInstanceOf(
      PasskeyUnsupported,
    );
    expect(mocked.passkeyRegisterFinish).not.toHaveBeenCalled();
    expect(mocked.passkeyRegisterAbort).toHaveBeenCalledWith("cer-1");
  });

  it("drops the challenge when the user dismisses the dialog", async () => {
    stubBrowser(null, null);
    (navigator.credentials.create as Mock).mockRejectedValue(
      new DOMException("cancelled", "NotAllowedError"),
    );
    mocked.passkeyRegisterBegin.mockResolvedValue(beginBody);
    mocked.passkeyRegisterAbort.mockResolvedValue(undefined);

    await expect(enrollPasskey("Phone")).rejects.toBeInstanceOf(DOMException);
    expect(mocked.passkeyRegisterAbort).toHaveBeenCalledWith("cer-1");
    expect(mocked.passkeyRegisterFinish).not.toHaveBeenCalled();
  });

  it("refuses to start where WebAuthn cannot work", async () => {
    vi.stubGlobal("window", { isSecureContext: false });
    await expect(enrollPasskey("Phone")).rejects.toBeInstanceOf(
      PasskeyUnsupported,
    );
    expect(mocked.passkeyRegisterBegin).not.toHaveBeenCalled();
  });
});

describe("sign-in", () => {
  it("offers every credential its own salt", async () => {
    const secret = new Uint8Array(32).fill(3).buffer;
    stubBrowser(null, fakeCredential({ results: { first: secret } }, "get"));
    mocked.passkeyLoginBegin.mockResolvedValue({
      ceremony: "cer-2",
      options: {
        publicKey: {
          challenge: toBase64url(new Uint8Array([1, 2])),
          allowCredentials: [
            { id: "AQID", type: "public-key" },
            { id: "BAUG", type: "public-key" },
          ],
        },
      },
      prf_salts: {
        AQID: toBase64url(new Uint8Array([1])),
        BAUG: toBase64url(new Uint8Array([2])),
      },
    });
    mocked.passkeyLoginFinish.mockResolvedValue({ username: "ralf" });

    expect(await signInWithPasskey()).toBe("ralf");

    const asked = firstCall(navigator.credentials.get as Mock);
    const byCredential = asked.publicKey.extensions.prf.evalByCredential;
    expect(Object.keys(byCredential).sort()).toEqual(["AQID", "BAUG"]);
    expect(Array.from(new Uint8Array(byCredential.AQID.first))).toEqual([1]);

    const sent = firstCall(mocked.passkeyLoginFinish);
    expect(sent.ceremony).toBe("cer-2");
    expect(sent.unlock_key).toBe(await deriveUnlockKey(secret));
    expect(sent.credential.response.signature).toBeTruthy();
  });

  it("does not sign in when the device returns no key material", async () => {
    stubBrowser(null, fakeCredential(undefined, "get"));
    mocked.passkeyLoginBegin.mockResolvedValue({
      ceremony: "cer-2",
      options: { publicKey: { challenge: "AQ", allowCredentials: [] } },
      prf_salts: {},
    });

    await expect(signInWithPasskey()).rejects.toBeInstanceOf(
      PasskeyUnsupported,
    );
    expect(mocked.passkeyLoginFinish).not.toHaveBeenCalled();
  });
});

// The point of fetching ahead: an extension provider only gets the prompt while
// the click still counts as a user gesture, and a round trip ends that.
describe("challenges fetched ahead of the click", () => {
  const loginBody = {
    ceremony: "cer-2",
    options: { publicKey: { challenge: "AQ", allowCredentials: [] } },
    prf_salts: {},
  };

  /** Lets the prefetch's promise chain settle. */
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

  it("opens the enrollment prompt in the same turn as the call", async () => {
    stubBrowser(
      fakeCredential({ results: { first: new Uint8Array(32).buffer } }),
      null,
    );
    mocked.passkeyRegisterBegin.mockResolvedValue(beginBody);
    mocked.passkeyRegisterFinish.mockResolvedValue(undefined);
    prepareEnrollment();
    await settle();

    const done = enrollPasskey("Phone");
    expect(navigator.credentials.create).toHaveBeenCalledTimes(1);
    await done;
    expect(mocked.passkeyRegisterBegin).toHaveBeenCalledTimes(1);
    expect(firstCall(mocked.passkeyRegisterFinish).ceremony).toBe("cer-1");
  });

  it("opens the sign-in prompt in the same turn as the call", async () => {
    stubBrowser(null, fakeCredential(undefined, "get"));
    mocked.passkeyLoginBegin.mockResolvedValue(loginBody);
    prepareSignIn();
    await settle();

    const done = signInWithPasskey();
    expect(navigator.credentials.get).toHaveBeenCalledTimes(1);
    await expect(done).rejects.toBeInstanceOf(PasskeyUnsupported);
  });

  it("hands a challenge out once, and fetches another after a failure", async () => {
    stubBrowser(null, fakeCredential(undefined, "get"));
    mocked.passkeyLoginBegin.mockResolvedValue(loginBody);
    prepareSignIn();
    await settle();

    await expect(signInWithPasskey()).rejects.toBeInstanceOf(PasskeyUnsupported);
    // Replaced straight away, so the next click is in the gesture as well.
    expect(mocked.passkeyLoginBegin).toHaveBeenCalledTimes(2);
    await settle();

    const done = signInWithPasskey();
    expect(navigator.credentials.get).toHaveBeenCalledTimes(2);
    await expect(done).rejects.toBeInstanceOf(PasskeyUnsupported);
  });

  it("replaces a challenge before the server forgets it", async () => {
    vi.useFakeTimers();
    mocked.passkeyRegisterBegin
      .mockResolvedValueOnce(beginBody)
      .mockResolvedValueOnce({ ...beginBody, ceremony: "cer-3" });
    mocked.passkeyRegisterAbort.mockResolvedValue(undefined);
    prepareEnrollment();
    await vi.advanceTimersByTimeAsync(4 * 60 * 1000);

    expect(mocked.passkeyRegisterBegin).toHaveBeenCalledTimes(2);
    // The superseded one is dropped rather than left on the server.
    expect(mocked.passkeyRegisterAbort).toHaveBeenCalledWith("cer-1");
  });

  it("does not use a challenge that may already have expired", async () => {
    vi.useFakeTimers();
    stubBrowser(null, null);
    // The refresh fails, so the first challenge is the only one there is.
    mocked.passkeyRegisterBegin
      .mockResolvedValueOnce(beginBody)
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(beginBody);
    prepareEnrollment();
    await vi.advanceTimersByTimeAsync(4.5 * 60 * 1000);

    void enrollPasskey("Phone").catch(() => undefined);
    expect(navigator.credentials.create).not.toHaveBeenCalled();
    expect(mocked.passkeyRegisterBegin).toHaveBeenCalledTimes(3);
  });

  it("lets go of the challenge when the screen closes", async () => {
    mocked.passkeyRegisterBegin.mockResolvedValue(beginBody);
    mocked.passkeyRegisterAbort.mockResolvedValue(undefined);
    prepareEnrollment();
    await settle();

    dropEnrollment();
    expect(mocked.passkeyRegisterAbort).toHaveBeenCalledWith("cer-1");
  });
});
