import { describe, expect, it } from "vitest";
import { handleVideoKey, type Player } from "./videokeys";

function player(at: number, duration = 60, paused = false) {
  const p = {
    currentTime: at,
    duration,
    paused,
    plays: 0,
    play() {
      p.plays++;
      p.paused = false;
      return Promise.resolve();
    },
    pause() {
      p.paused = true;
    },
  };
  return p;
}

function key(k: string, mods: Partial<KeyboardEvent> = {}): KeyboardEvent {
  return { key: k, metaKey: false, ctrlKey: false, altKey: false, ...mods } as KeyboardEvent;
}

const FRAME = 1 / 25;

describe("video keys", () => {
  it("k toggles playback", () => {
    const p = player(10);
    expect(handleVideoKey(p as Player, key("k"), FRAME)).toBe(true);
    expect(p.paused).toBe(true);
    handleVideoKey(p as Player, key("k"), FRAME);
    expect(p.plays).toBe(1);
    expect(p.paused).toBe(false);
  });

  it("j and l jump five seconds and keep playing", () => {
    const p = player(10);
    handleVideoKey(p as Player, key("l"), FRAME);
    expect(p.currentTime).toBe(15);
    handleVideoKey(p as Player, key("j"), FRAME);
    handleVideoKey(p as Player, key("j"), FRAME);
    expect(p.currentTime).toBe(5);
    expect(p.paused).toBe(false);
  });

  it("stays inside the clip", () => {
    const p = player(2, 8);
    handleVideoKey(p as Player, key("j"), FRAME);
    expect(p.currentTime).toBe(0);
    p.currentTime = 6;
    handleVideoKey(p as Player, key("l"), FRAME);
    expect(p.currentTime).toBe(8);
  });

  // Shift turns the letter upper case; that is the frame step.
  it("with Shift steps a single frame, paused", () => {
    const p = player(1);
    handleVideoKey(p as Player, key("L", { shiftKey: true }), FRAME);
    expect(p.paused).toBe(true);
    expect(p.currentTime).toBeCloseTo(1 + FRAME);
    handleVideoKey(p as Player, key("J", { shiftKey: true }), FRAME);
    handleVideoKey(p as Player, key("J", { shiftKey: true }), FRAME);
    expect(p.currentTime).toBeCloseTo(1 - FRAME);
  });

  it("leaves browser shortcuts and other keys alone", () => {
    const p = player(10);
    expect(handleVideoKey(p as Player, key("l", { metaKey: true }), FRAME)).toBe(false);
    expect(handleVideoKey(p as Player, key("k", { ctrlKey: true }), FRAME)).toBe(false);
    expect(handleVideoKey(p as Player, key("f"), FRAME)).toBe(false);
    expect(p.currentTime).toBe(10);
    expect(p.paused).toBe(false);
  });
});
