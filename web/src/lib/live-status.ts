// Status do canal ao vivo do quadro, partilhado entre o canal (lib/live) e
// a pílula do header. O estado é um valor de módulo (um por aba): o canal
// escreve, a pílula lê, e nenhum dos dois precisa de conhecer o outro além
// deste store.
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

/** A pílula do header assina o status do canal: "Ao vivo" com o stream
 *  aberto, "Reconectando" quando ele cai e tenta de novo, "Offline" quando
 *  não há stream. */
export function useLiveStatus(): LiveStatus {
  return React.useSyncExternalStore(subscribe, snapshot);
}
