// Status do canal ao vivo do quadro (lib/live). O estado é um valor de
// módulo (um por aba): o canal escreve, os assinantes leem (o intervalo do
// quadro em lib/queries e as caixas de teste), e nenhuma das partes precisa
// de conhecer as outras além deste store.
import * as React from "react";

export type LiveStatus = "live" | "reconnecting" | "offline";

type Listener = () => void;

const listeners = new Set<Listener>();
let current: LiveStatus = "offline";

/** Só o canal (lib/live) escreve o status: `live` com o stream aberto,
 *  `reconnecting` enquanto o EventSource tenta (re)conectar e `offline`
 *  sem stream (sem sessão ou queda definitiva, com a recaída por consulta). */
export function setLiveStatus(next: LiveStatus): void {
  if (next === current) return;
  current = next;
  listeners.forEach((listener) => listener());
}

function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function snapshot(): LiveStatus {
  return current;
}

/** Lê o status sem React (probes e testes). */
export function liveStatusSnapshot(): LiveStatus {
  return snapshot();
}

/** Assina o status do canal: "live" com o stream aberto, "reconnecting"
 *  quando ele cai e tenta de novo e "offline" quando não há stream (a
 *  leitura que o intervalo do quadro usa em lib/queries). */
export function useLiveStatus(): LiveStatus {
  return React.useSyncExternalStore(subscribe, snapshot);
}
