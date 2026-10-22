// Keyboard control of the lightbox player.
//
// j / k / l as on YouTube, since that is where the habit comes from: back,
// play/pause, forward. With Shift the jump shrinks to a single frame, which is
// what picking a thumbnail actually needs -- five seconds is far too coarse to
// land on the moment someone's eyes are open.

/** How far j and l jump. */
export const SKIP_SECONDS = 5;

/** Assumed until the player has shown enough frames to measure. */
const DEFAULT_FRAME = 1 / 30;

/** The subset of a media element this needs, so tests can pass a plain object. */
export type Player = Pick<HTMLVideoElement, "currentTime" | "duration" | "paused" | "play" | "pause">;

/**
 * Applies a key to the player. Returns whether the key was one of ours, so the
 * caller can stop it doing anything else.
 */
export function handleVideoKey(el: Player, e: KeyboardEvent, frame: number): boolean {
  // Cmd+L is the address bar and Ctrl+K a browser search; neither is a seek.
  if (e.metaKey || e.ctrlKey || e.altKey) return false;

  switch (e.key) {
    case "k":
    case "K":
      if (el.paused) {
        // Rejected when the browser wants a gesture first; nothing to do then.
        el.play().catch(() => {});
      } else {
        el.pause();
      }
      return true;
    case "j":
      seek(el, -SKIP_SECONDS);
      return true;
    case "l":
      seek(el, SKIP_SECONDS);
      return true;
    case "J":
    case "L":
      // Stepping a playing video would be overtaken by the next frame at once.
      el.pause();
      seek(el, e.key === "J" ? -frame : frame);
      return true;
  }
  return false;
}

function seek(el: Player, by: number): void {
  const end = Number.isFinite(el.duration) ? el.duration : Infinity;
  el.currentTime = Math.min(end, Math.max(0, el.currentTime + by));
}

/**
 * Measures a video's frame duration as it plays.
 *
 * The element does not say what its frame rate is. requestVideoFrameCallback
 * reports each presented frame with its media time, and two consecutive frames
 * give the interval exactly. Without it, or before two frames have been shown,
 * 30 fps is assumed: on a 60 fps phone clip a step then skips one frame, which
 * is still fine enough to choose a still by.
 */
export function trackFrames(el: HTMLVideoElement): { frame(): number; stop(): void } {
  let frame = DEFAULT_FRAME;
  if (typeof el.requestVideoFrameCallback !== "function") {
    return { frame: () => frame, stop: () => {} };
  }

  let prev: VideoFrameCallbackMetadata | null = null;
  let handle = 0;
  const onFrame: VideoFrameRequestCallback = (_now, meta) => {
    // Only adjacent frames: a seek or a dropped frame spans several, and the
    // interval would then be a multiple of the real one.
    if (prev && meta.presentedFrames - prev.presentedFrames === 1) {
      const dt = meta.mediaTime - prev.mediaTime;
      if (dt > 0 && dt < 1) frame = dt;
    }
    prev = meta;
    handle = el.requestVideoFrameCallback(onFrame);
  };
  handle = el.requestVideoFrameCallback(onFrame);

  return {
    frame: () => frame,
    stop: () => el.cancelVideoFrameCallback(handle),
  };
}
