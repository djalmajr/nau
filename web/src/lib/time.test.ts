import { describe, expect, it } from "vitest";
import { absoluteTime, hourLabel, relativeTime } from "./time";

const NOW = 1_793_376_000_000;
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

describe("relativeTime", () => {
  it("uses the locale and the just-now label in the chosen language", () => {
    expect(relativeTime(NOW - 3 * MINUTE, "pt-BR", NOW, "agora")).toBe("há 3 minutos");
    expect(relativeTime(NOW - 3 * MINUTE, "en-US", NOW, "just now")).toBe("3 minutes ago");
  });

  it("steps through minutes, hours, days and falls back to the absolute date", () => {
    expect(relativeTime(NOW - 10 * MINUTE, "pt-BR", NOW, "agora")).toBe("há 10 minutos");
    expect(relativeTime(NOW - 3 * HOUR, "pt-BR", NOW, "agora")).toBe("há 3 horas");
    expect(relativeTime(NOW - 3 * DAY, "pt-BR", NOW, "agora")).toBe("há 3 dias");
    const old = relativeTime(NOW - 40 * DAY, "pt-BR", NOW, "agora");
    expect(old).toBe(absoluteTime(NOW - 40 * DAY, "pt-BR"));
  });

  it("clamps the future and very recent values to just now", () => {
    expect(relativeTime(NOW + 5 * MINUTE, "pt-BR", NOW, "agora")).toBe("agora");
    expect(relativeTime(NOW - 20_000, "en-US", NOW, "just now")).toBe("just now");
  });
});

describe("absoluteTime and hourLabel", () => {
  it("formats a stable absolute date-time and a 24h clock label", () => {
    const pt = absoluteTime(NOW, "pt-BR");
    const en = absoluteTime(NOW, "en-US");
    expect(pt).not.toBe(en);
    expect(pt).toContain("2026");
    expect(hourLabel(NOW, "pt-BR")).toMatch(/^\d{2}:\d{2}$/);
    expect(hourLabel(NOW + HOUR, "en-US")).toMatch(/^\d{2}:\d{2}$/);
  });
});
