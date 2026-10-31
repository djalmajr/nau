// Regras puras da tela de agentes: ordenação, filtro e somas de ocultos.
// Sem React para as mesmas regras servirem ao Board, à Lista e aos testes.
import type { Agent, AgentState, Counts, HiddenCounts } from "./api";

/** Colunas do Board, na ordem exibida (o `unknown` fica fora das colunas). */
export const BOARD_COLUMNS: AgentState[] = ["needs_you", "working", "done", "idle"];

/** Ordem completa de estados: colunas do Board e, por último, o desconhecido. */
export const STATE_ORDER: AgentState[] = [...BOARD_COLUMNS, "unknown"];

export function isKnownState(state: string): state is AgentState {
  return (STATE_ORDER as string[]).includes(state);
}

/** Momento de referência "desde quando": `since_ms`, ou `observed_ms` quando
 *  `since_ms` é null. */
export function agentSinceMs(agent: Agent): number {
  return agent.since_ms ?? agent.observed_ms ?? 0;
}

/** Ordena um estado: mais antigo primeiro (o cartão mais antigo fica em
 *  cima); empate pelo id para ordem estável. */
function sortWithinState(list: Agent[]): Agent[] {
  return [...list].sort(
    (a, b) => agentSinceMs(a) - agentSinceMs(b) || a.id.localeCompare(b.id),
  );
}

/** Agentes agrupados por estado, cada grupo ordenado (mais antigo primeiro). */
export function agentsByState(agents: Agent[]): Map<AgentState, Agent[]> {
  const by = new Map<AgentState, Agent[]>(STATE_ORDER.map((state) => [state, []]));
  for (const agent of agents) {
    const state: AgentState = isKnownState(agent.state) ? agent.state : "unknown";
    by.get(state)?.push(agent);
  }
  for (const [state, list] of by) by.set(state, sortWithinState(list));
  return by;
}

/** A mesma ordem do Board em lista única: por estado e depois por `since_ms`. */
export function agentsInStateOrder(agents: Agent[]): Agent[] {
  const by = agentsByState(agents);
  return STATE_ORDER.flatMap((state) => by.get(state) ?? []);
}

export interface AgentFilters {
  developer: string;
  machine: string;
  project: string;
  q: string;
}

export const EMPTY_FILTERS: AgentFilters = { developer: "", machine: "", project: "", q: "" };

/** Filtro local da lista completa (o contrato deixa a UI fazer isso):
 *  ids de desenvolvedor/máquina, nome do projeto e busca textual por tarefa
 *  ou nome do agente (sem diferenciar maiúsculas). */
export function filterAgents(agents: Agent[], filters: AgentFilters): Agent[] {
  const query = filters.q.trim().toLowerCase();
  return agents.filter((agent) => {
    if (filters.developer && agent.developer.id !== filters.developer) return false;
    if (filters.machine && agent.machine.id !== filters.machine) return false;
    if (filters.project && agent.project !== filters.project) return false;
    if (query) {
      const haystack = `${agent.task ?? ""} ${agent.name}`.toLowerCase();
      if (!haystack.includes(query)) return false;
    }
    return true;
  });
}

export function countsTotal(counts: Counts): number {
  return counts.working + counts.needs_you + counts.done + counts.idle + counts.unknown;
}

/** Soma total de agentes em projetos ocultos. */
export function hiddenTotal(hidden: HiddenCounts[]): number {
  return hidden.reduce((sum, entry) => sum + countsTotal(entry.counts), 0);
}

/** Soma por estado dos `hidden` — alimenta o cartão de ocultos de cada coluna. */
export function hiddenTotalByState(hidden: HiddenCounts[]): Record<AgentState, number> {
  const by = Object.fromEntries(STATE_ORDER.map((state) => [state, 0])) as Record<AgentState, number>;
  for (const entry of hidden) {
    for (const state of STATE_ORDER) by[state] += entry.counts[state];
  }
  return by;
}

export interface Option {
  id: string;
  name: string;
}

/** Opções únicas (id + nome) de uma lista, ordenadas pelo nome para o
 *  seletor; id vazio nunca vira opção. */
export function uniqueOptions<T>(items: T[], id: (item: T) => string, name: (item: T) => string): Option[] {
  const seen = new Map<string, Option>();
  for (const item of items) {
    const optionId = id(item);
    if (!optionId || seen.has(optionId)) continue;
    seen.set(optionId, { id: optionId, name: name(item) });
  }
  return [...seen.values()].sort((a, b) => a.name.localeCompare(b.name));
}
