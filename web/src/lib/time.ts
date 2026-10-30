// Tempos relativos e absolutos no idioma escolhido (Intl). O tempo "agora"
// pode ser injetado (nowMs) para os testes; em runtime é Date.now().
import type { Locale } from "./i18n";

const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;

/** Tempo relativo ("há 3 min") via Intl.RelativeTimeFormat no idioma.
 *  `justNow` é a tradução de "agora" (a UI passa a do dicionário); sem ele,
 *  menos de 45 s não tem unidade e vira a própria marca. Segundo o contrato,
 *  `observed_ms` é o instante do último inventário, então um valor no futuro
 *  (relogios divergentes) não vira "em X min": corta em 0. */
export function relativeTime(
  ms: number,
  locale: Locale,
  nowMs: number = Date.now(),
  justNow: string = "",
): string {
  const delta = Math.max(0, nowMs - ms);
  if (delta < 45_000) return justNow || "now";
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  if (delta < 90 * MINUTE_MS) return rtf.format(-Math.round(delta / MINUTE_MS), "minute");
  if (delta < 22 * HOUR_MS) return rtf.format(-Math.round(delta / HOUR_MS), "hour");
  if (delta < 30 * DAY_MS) return rtf.format(-Math.round(delta / DAY_MS), "day");
  return absoluteTime(ms, locale);
}

/** Tempo absoluto compacto (dia/mês/ano + hora:minuto) no idioma. */
export function absoluteTime(ms: number, locale: Locale): string {
  return new Intl.DateTimeFormat(locale, {
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(ms));
}

/** Rótulo de hora (hh:mm, 24 h) para as marcas do gráfico de 24 h. */
export function hourLabel(ms: number, locale: Locale): string {
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    hourCycle: "h23",
    minute: "2-digit",
  }).format(new Date(ms));
}
