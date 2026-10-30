// Parâmetros de busca da rota de agentes. A troca Board | Lista e os filtros
// vivem na URL (`view`, `developer`, `machine`, `project`, `q`) — o detalhe
// do agente recebe os mesmos parâmetros para a barra de volta preservá-los.
// Valor inválido volta ao padrão: `view=board`, sem filtro.
import type { Agent, AgentsResponse } from "./api";
import { uniqueOptions, type Option } from "./agents";

export type AgentsView = "board" | "list";

export interface AgentsSearch {
  /** ausente = board (o padrão); "list" ativa a lista. */
  view?: AgentsView;
  developer?: string;
  machine?: string;
  project?: string;
  q?: string;
}

/** `validateSearch` compartilhado por /agents e /agents/$agentId. */
export function parseAgentsSearch(search: Record<string, unknown>): AgentsSearch {
  const str = (value: unknown): string => (typeof value === "string" ? value : "");
  const view: AgentsView | undefined =
    search.view === "list" || search.view === "board" ? (search.view as AgentsView) : undefined;
  return {
    view,
    developer: str(search.developer) || undefined,
    machine: str(search.machine) || undefined,
    project: str(search.project) || undefined,
    q: str(search.q) || undefined,
  };
}

/** View efetiva: ausente/inválida volta ao padrão board. */
export function effectiveView(search: AgentsSearch): AgentsView {
  return search.view ?? "board";
}

/** Opções dos filtros, tiradas da lista recebida (mais "Todos" na UI):
 *  desenvolvedor e projeto dos agentes visíveis; máquina também dos
 *  projetos ocultos (a única fonte onde aparecem). */
export interface FilterOptions {
  developers: Option[];
  machines: Option[];
  projects: Option[];
}

export function filterOptions(data: AgentsResponse): FilterOptions {
  const machineItems = [
    ...data.agents.map((agent: Agent) => agent.machine),
    ...data.hidden.map((entry) => entry.machine),
  ];
  return {
    developers: uniqueOptions(data.agents, (agent) => agent.developer.id, (agent) => agent.developer.name),
    machines: uniqueOptions(machineItems, (machine) => machine.id, (machine) => machine.name),
    projects: uniqueOptions(data.agents, (agent) => agent.project, (agent) => agent.project),
  };
}
