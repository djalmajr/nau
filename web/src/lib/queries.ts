// Queries TanStack Query dos endpoints de leitura do hub. O tempo real
// chega num card seguinte; por enquanto a atualização periódica de 15 s
// mantém as telas vivas (a pílula do header continua "offline").
import { useQuery } from "@tanstack/react-query";
import { api } from "./api";

/** Intervalo de atualização periódica das telas de dados. */
export const AGENT_REFRESH_MS = 15_000;

export const AGENTS_QUERY_KEY = ["agents", "list"] as const;
export const AGENT_QUERY_KEY = (id: string) => ["agents", id] as const;
export const OVERVIEW_QUERY_KEY = ["overview"] as const;

/** GET /api/agents — a UI filtra a lista completa no cliente. */
export function useAgents() {
  return useQuery({
    queryKey: AGENTS_QUERY_KEY,
    queryFn: () => api.getAgents(),
    refetchInterval: AGENT_REFRESH_MS,
  });
}

/** GET /api/agents/{id} — 404 vira ApiError(404) na UI (tela "não encontrado"). */
export function useAgent(id: string) {
  return useQuery({
    queryKey: AGENT_QUERY_KEY(id),
    queryFn: () => api.getAgent(id),
    refetchInterval: AGENT_REFRESH_MS,
  });
}

/** GET /api/overview — números, espera, máquinas, 24 h e projetos. */
export function useOverview() {
  return useQuery({
    queryKey: OVERVIEW_QUERY_KEY,
    queryFn: () => api.getOverview(),
    refetchInterval: AGENT_REFRESH_MS,
  });
}
