// Queries TanStack Query dos endpoints de leitura do hub. O tempo real
// chega num card seguinte; por enquanto a atualização periódica de 15 s
// mantém as telas vivas (a pílula do header continua "offline").
// O `signal` da query vai até o fetch (lib/api) e a id do usuário, que já
// está na chave, vai junto na chamada: o 401 atrasado de uma conta anterior
// é cancelado no clear() e/ou ignorado pelo handler central.
import { useQuery } from "@tanstack/react-query";
import { api } from "./api";
import { useSession } from "./session";

/** Intervalo de atualização periódica das telas de dados. */
export const AGENT_REFRESH_MS = 15_000;

// Chaves namespaced: lista e detalhe nunca colidem (um agente com id "list"
// não pega a chave da lista) e a id do usuário da sessão entra na chave como
// segunda defesa — a primeira é limpar o QueryClient em logout/401/login
// (lib/session, app-shell e login). Os dados de uma conta nunca chegam à
// chave de outra.
export const AGENTS_QUERY_KEY = (userId: string) => ["agents", "list", userId] as const;
export const AGENT_QUERY_KEY = (userId: string, id: string) => ["agents", "detail", userId, id] as const;
export const OVERVIEW_QUERY_KEY = (userId: string) => ["overview", userId] as const;

/** Id do usuário da sessão, lido da query de sessão já em cache. Indefinido
 *  enquanto a sessão é verificada: as queries ficam desabilitadas (sem
 *  requisição vazia) até o usuário ser conhecido. */
function useSessionUserId(): string | undefined {
  const { data } = useSession();
  return data?.id;
}

/** GET /api/agents — a UI filtra a lista completa no cliente. */
export function useAgents() {
  const userId = useSessionUserId();
  return useQuery({
    queryKey: AGENTS_QUERY_KEY(userId ?? "anonymous"),
    queryFn: ({ signal }) => api.getAgents({ signal, user: userId }),
    refetchInterval: AGENT_REFRESH_MS,
    enabled: userId !== undefined,
  });
}

/** GET /api/agents/{id} — 404 vira ApiError(404) na UI (tela "não encontrado"). */
export function useAgent(id: string) {
  const userId = useSessionUserId();
  return useQuery({
    queryKey: AGENT_QUERY_KEY(userId ?? "anonymous", id),
    queryFn: ({ signal }) => api.getAgent(id, { signal, user: userId }),
    refetchInterval: AGENT_REFRESH_MS,
    enabled: userId !== undefined,
  });
}

/** GET /api/overview — números, espera, máquinas, 24 h e projetos. */
export function useOverview() {
  const userId = useSessionUserId();
  return useQuery({
    queryKey: OVERVIEW_QUERY_KEY(userId ?? "anonymous"),
    queryFn: ({ signal }) => api.getOverview({ signal, user: userId }),
    refetchInterval: AGENT_REFRESH_MS,
    enabled: userId !== undefined,
  });
}
