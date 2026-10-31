// Canal ao vivo do quadro: um EventSource por aba, aberto para a conta
// logada e fechado no logout, na troca de conta e no 401 de sessão.
//
// Com o stream aberto, cada evento `board` traz a nova versão do quadro e
// invalida as queries da conta (lib/queries.invalidateBoardQueries) — a
// tela atualiza sem recarregar. Enquanto o stream tenta (re)conectar, as
// queries do quadro mantêm o intervalo de hoje. Quando o stream não abre
// (resposta rejeitada do servidor ou três falhas seguidas), a recaída
// consulta GET /api/live a cada 15 s e invalida quando a versão muda.
import * as React from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, type SessionUser } from "./api";
import { AGENT_REFRESH_MS, invalidateBoardQueries } from "./queries";
import { SESSION_QUERY_KEY } from "./session";
import { setLiveStatus, useLiveStatus } from "./live-status";

// A chave da consulta de recaída inclui a conta: a leitura da conta
// anterior não pode invalidar (nem pintar) o quadro da nova.
const LIVE_VERSION_QUERY_KEY = (userId: string) => ["live", "version", userId] as const;

// Três falhas seguidas do EventSource fecham o stream e ligam a recaída.
const MAX_CONSECUTIVE_STREAM_FAILURES = 3;

/** A id da sessão como o canal precisa vê-la: lê o estado da query de
 *  sessão no cache e reage a cada evento do cache daquela query. Sem ela,
 *  o canal abriria stream para a conta errada — ou nenhuma — ou deixaria o
 *  stream da conta anterior aberto.
 *
 *  Não usa o observer do `useSession()` como fonte: quando a query é
 *  removida do cache (o `queryClient.clear()` do logout e do 401 central),
 *  o observer congela com o dado antigo e o canal nunca veria a sessão
 *  sumir. A assinatura do cache entrega o evento `removed` e o canal fecha
 *  o stream. Sessão some do cache, muda para outra conta ou a query erra
 *  (401 incluído) → `undefined`: sem sessão, sem stream nesta aba. */
function useChannelSession(queryClient: QueryClient): string | undefined {
  const [, force] = React.useReducer((n: number) => n + 1, 0);
  React.useEffect(() => {
    const cache = queryClient.getQueryCache();
    const unsubscribe = cache.subscribe((event) => {
      if (!Array.isArray(event.query.queryKey) || event.query.queryKey[0] !== SESSION_QUERY_KEY[0]) return;
      force();
    });
    return unsubscribe;
  }, [queryClient]);
  const cache = queryClient.getQueryCache();
  const query = cache.find({ queryKey: SESSION_QUERY_KEY });
  // A query do cache é genérica: o dado é a sessão (ou ausente) e o erro,
  // quando existe, é o ApiError do fetch.
  const data = query?.state.data as SessionUser | null | undefined;
  const error = query?.state.error;
  if (data === null || data === undefined || error !== null) {
    return undefined;
  }
  return data.id;
}

/** O canal: montado uma vez pela raiz do app (e pelo harness de teste),
 *  cuida do stream da sessão logada e da recaída por consulta. Não
 *  renderiza nada. */
export function LiveChannel() {
  const queryClient = useQueryClient();
  const userId = useChannelSession(queryClient);

  // Última versão aplicada (evento ou leitura da recaída): repetir a mesma
  // versão não invalida de novo.
  const appliedVersion = React.useRef<string | null>(null);
  // A sessão para a qual o canal já decidiu (null: ainda nenhuma). A recaída
  // só consulta depois de o efeito abaixo fixar o status para a sessão
  // atual: o status do store pode ser o "offline" de antes (a montagem sem
  // sessão, a conta anterior), e a render que traz a sessão nova chega antes
  // do efeito que abre o stream dela.
  const [decidedFor, setDecidedFor] = React.useState<string | undefined | null>(null);

  React.useEffect(() => {
    appliedVersion.current = null;
    setDecidedFor(userId);
    if (userId === undefined) {
      // Sem sessão (tela de login, logout, 401): nenhum stream nesta aba.
      setLiveStatus("offline");
      return;
    }
    if (typeof EventSource === "undefined") {
      // Sem suporte a EventSource: a recaída por consulta assume.
      setLiveStatus("offline");
      return;
    }
    const es = new EventSource("/api/events");
    let consecutiveFailures = 0;

    es.addEventListener("open", () => {
      consecutiveFailures = 0;
      setLiveStatus("live");
    });

    es.addEventListener("error", () => {
      if (es.readyState === EventSource.CLOSED) {
        // O servidor rejeitou o stream (401, 503 indisponível, 429 limite):
        // não há retry automático do EventSource — cai a recaída por
        // consulta, que continua funcionando com a sessão da UI.
        setLiveStatus("offline");
        return;
      }
      consecutiveFailures += 1;
      if (consecutiveFailures >= MAX_CONSECUTIVE_STREAM_FAILURES) {
        es.close();
        setLiveStatus("offline");
        return;
      }
      // O stream caiu e o próprio EventSource tenta reconectar.
      setLiveStatus("reconnecting");
    });

    es.addEventListener("board", (event) => {
      const raw = (event as MessageEvent<string>).data;
      let version: unknown;
      try {
        version = (JSON.parse(raw) as { version?: unknown }).version;
      } catch {
        return;
      }
      if (typeof version !== "string" || version === "") return;
      // Separação por conta (a decisão de isolamento): o evento é aplicado
      // só se a sessão atual é a que abriu este stream. A sessão atual é
      // lida no momento do evento, direto do cache — a mesma cautela do
      // handler central de 401 — de modo que um evento atrasado da conta
      // anterior (depois de logout ou de troca de conta) é ignorado, sem
      // vazar dado nem invalidar o quadro errado.
      const session = queryClient.getQueryData<SessionUser>(SESSION_QUERY_KEY);
      if (!session || session.id !== userId) return;
      if (version === appliedVersion.current) return;
      appliedVersion.current = version;
      // Invalida as queries da sessão atual (que a guarda acima garantiu
      // ser a do stream): nunca as de outra conta, mesmo que um evento
      // atrasado da conta anterior chegue depois de logout ou troca.
      invalidateBoardQueries(queryClient, session.id);
    });

    setLiveStatus("reconnecting");
    return () => {
      es.close();
      // Sem sessão (logout/troca de conta) ou fim do componente: sem
      // stream; se a troca foi para outra conta logada, o próximo efeito
      // reabre e volta a "reconnecting".
      setLiveStatus("offline");
    };
  }, [userId, queryClient]);

  // Recaída: apenas sem stream (o EventSource fechado ou ausente) e com
  // sessão; um 401 do /api/live passa pelo handler central de 401 que já
  // existe em lib/api.
  const status = useLiveStatus();
  const fallback = useQuery({
    queryKey: LIVE_VERSION_QUERY_KEY(userId ?? "anonymous"),
    queryFn: ({ signal }) => api.getLiveVersion({ signal, user: userId }),
    enabled: userId !== undefined && decidedFor === userId && status === "offline",
    refetchInterval: AGENT_REFRESH_MS,
    retry: false,
  });

  React.useEffect(() => {
    const version = fallback.data?.version;
    if (userId === undefined || version === undefined) return;
    // A primeira leitura é a linha de base, não uma mudança: ela registra
    // a versão sem invalidar. Só um valor diferente da última versão
    // aplicada (evento `board` ou leitura anterior) invalida o quadro.
    if (appliedVersion.current === null) {
      appliedVersion.current = version;
      return;
    }
    if (version === appliedVersion.current) return;
    appliedVersion.current = version;
    invalidateBoardQueries(queryClient, userId);
  }, [fallback.data, userId, queryClient]);

  return null;
}
