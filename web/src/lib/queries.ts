// Queries TanStack Query dos endpoints de leitura do hub. O tempo real
// chega num card seguinte; por enquanto a atualização periódica de 15 s
// mantém as telas vivas (a pílula do header continua "offline").
// O `signal` da query vai até o fetch (lib/api) e a id do usuário, que já
// está na chave, vai junto na chamada: o 401 atrasado de uma conta anterior
// é cancelado no clear() e/ou ignorado pelo handler central.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
export const MACHINES_QUERY_KEY = (userId: string) => ["machines", "list", userId] as const;
export const DEVELOPERS_QUERY_KEY = (userId: string) => ["developers", userId] as const;
export const INVITES_QUERY_KEY = (userId: string) => ["invites", userId] as const;
export const VERSION_QUERY_KEY = (userId: string) => ["version", userId] as const;

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

/** GET /api/machines — as próprias + as compartilhadas. */
export function useMachines() {
  const userId = useSessionUserId();
  return useQuery({
    queryKey: MACHINES_QUERY_KEY(userId ?? "anonymous"),
    queryFn: ({ signal }) => api.getMachines({ signal, user: userId }),
    refetchInterval: AGENT_REFRESH_MS,
    enabled: userId !== undefined,
  });
}

/** GET /api/developers — contas do hub e as contagens de compartilhamento. */
export function useDevelopers() {
  const userId = useSessionUserId();
  return useQuery({
    queryKey: DEVELOPERS_QUERY_KEY(userId ?? "anonymous"),
    queryFn: ({ signal }) => api.getDevelopers({ signal, user: userId }),
    enabled: userId !== undefined,
  });
}

/** GET /api/invites — convites pendentes (owner; a seção some para membro). */
export function useInvites() {
  const userId = useSessionUserId();
  return useQuery({
    queryKey: INVITES_QUERY_KEY(userId ?? "anonymous"),
    queryFn: ({ signal }) => api.getInvites({ signal, user: userId }),
    enabled: userId !== undefined,
  });
}

/** GET /api/version — versão do hub (não refaz em intervalo: muda com o hub,
 *  não com o estado dos agentes). */
export function useVersion() {
  const userId = useSessionUserId();
  return useQuery({
    queryKey: VERSION_QUERY_KEY(userId ?? "anonymous"),
    queryFn: ({ signal }) => api.getVersion({ signal, user: userId }),
    refetchInterval: false,
    enabled: userId !== undefined,
  });
}

// Mutações: cada uma invalida as queries que o efeito dela afeta (a chave
// leva a id do usuário, igual às queries acima). `signal` vem do TanStack
// (cancela a mutação em voo no unmount/clear); `user` alimenta o handler
// central de 401.

/** DELETE /api/machines/{id} — revoga a chave; atualiza a lista de máquinas
 *  (a máquina vira revogada) e a de agentes (os agentes dela ficam no último
 *  estado, máquina offline). */
/** DELETE /api/machines/{id} — revoga a chave. As contagens dos
 *  desenvolvedores mudam junto (a máquina some de lá), então a lista é
 *  invalidada também. */
export function useRevokeMachine() {
  const userId = useSessionUserId();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.revokeMachine(id, { user: userId }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: MACHINES_QUERY_KEY(userId ?? "anonymous") });
      void queryClient.invalidateQueries({ queryKey: AGENTS_QUERY_KEY(userId ?? "anonymous") });
      void queryClient.invalidateQueries({ queryKey: DEVELOPERS_QUERY_KEY(userId ?? "anonymous") });
    },
  });
}

/** POST /api/pairings/confirm — vincula o pareamento; a máquina nova aparece
 *  na lista. */
export function useConfirmPairing() {
  const userId = useSessionUserId();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (code: string) => api.confirmPairing(code, { user: userId }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: MACHINES_QUERY_KEY(userId ?? "anonymous") });
    },
  });
}

/** POST /api/machines/{id}/shares — o `shared_with` da máquina muda e as
 *  contagens dos desenvolvedores também (quem você compartilha com). */
export function useShareMachine() {
  const userId = useSessionUserId();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, email }: { id: string; email: string }) =>
      api.shareMachine(id, email, { user: userId }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: MACHINES_QUERY_KEY(userId ?? "anonymous") });
      void queryClient.invalidateQueries({ queryKey: DEVELOPERS_QUERY_KEY(userId ?? "anonymous") });
    },
  });
}

/** DELETE /api/machines/{id}/shares/{developerId} — remove um compartilhado
 *  (as contagens dos desenvolvedores mudam junto). */
export function useUnshareMachine() {
  const userId = useSessionUserId();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, developerId }: { id: string; developerId: string }) =>
      api.unshareMachine(id, developerId, { user: userId }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: MACHINES_QUERY_KEY(userId ?? "anonymous") });
      void queryClient.invalidateQueries({ queryKey: DEVELOPERS_QUERY_KEY(userId ?? "anonymous") });
    },
  });
}

/** POST /api/invites — cria um convite (o token some da resposta 201 e não
 *  volta em GET). */
export function useCreateInvite() {
  const userId = useSessionUserId();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (email: string) => api.createInvite(email, { user: userId }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: INVITES_QUERY_KEY(userId ?? "anonymous") });
    },
  });
}

/** DELETE /api/invites/{id} — revoga um convite pendente. */
export function useRevokeInvite() {
  const userId = useSessionUserId();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.revokeInvite(id, { user: userId }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: INVITES_QUERY_KEY(userId ?? "anonymous") });
    },
  });
}

/** POST /api/session/password — 204: o usuário é o mesmo, só a senha e a
 *  cookie mudam; nada no cache fica desatualizado. */
export function useChangePassword() {
  return useMutation({
    mutationFn: ({ current, newPassword }: { current: string; newPassword: string }) =>
      api.changePassword(current, newPassword),
  });
}
