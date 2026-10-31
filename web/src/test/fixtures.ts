// Dados neutros de teste (e do servidor falso da verificação visual), no
// formato exato do contrato (docs/hub-api.md). Cobre os cinco estados,
// máquina offline (pc), task null, since_ms null, report_ready e projetos
// ocultos. Nomes definidos no brief.
import type {
  Agent,
  AgentDetailResponse,
  AgentsResponse,
  Developer,
  Machine,
  OverviewResponse,
} from "@/lib/api";

// "Agora" dinâmico: os tempos relativos dos componentes ("há 3 min") ficam
// determinísticos nos testes sem importar o fuso de quem roda a suíte.
export const NOW_MS = Date.now();
const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;

export const ANA: Developer = { id: "u1", name: "Ana Exemplo" };
export const BRUNO: Developer = { id: "u2", name: "Bruno Exemplo" };

export const LAPTOP_A: Machine = { id: "m1", name: "laptop-a", online: true };
export const LAPTOP_B: Machine = { id: "m2", name: "laptop-b", online: true };
export const PC: Machine = { id: "m3", name: "pc", online: false };

function agent(partial: Partial<Agent> & Pick<Agent, "id" | "state" | "name">): Agent {
  return {
    effort: "medium",
    kind: "cli-a",
    machine: LAPTOP_A,
    model: "example/model-1",
    project: "acme/web",
    reason: null,
    report_ready: false,
    session: "w1",
    started_ms: NOW_MS - 2 * HOUR_MS,
    task: null,
    terminal: "w1:p1",
    developer: ANA,
    observed_ms: NOW_MS - MINUTE_MS,
    since_ms: NOW_MS - 5 * MINUTE_MS,
    role: "implementer",
    ...partial,
  };
}

export const AGENTS: Agent[] = [
  agent({
    developer: ANA,
    effort: "high",
    id: "m2.w1:p2",
    kind: "cli-b",
    machine: LAPTOP_B,
    name: "build",
    reason: "approval",
    report_ready: true,
    role: "implementer",
    since_ms: NOW_MS - 3 * MINUTE_MS,
    started_ms: NOW_MS - 2 * HOUR_MS,
    state: "needs_you",
    task: "Retry declined cards in the checkout worker",
    terminal: "w1:p2",
  }),
  agent({
    developer: BRUNO,
    id: "m1.w2:p1",
    model: "example/model-2",
    name: "scout",
    observed_ms: NOW_MS - 5 * MINUTE_MS,
    project: "acme/api",
    reason: "question",
    role: "scouter",
    since_ms: NOW_MS - 30 * MINUTE_MS,
    started_ms: NOW_MS - 4 * HOUR_MS,
    state: "needs_you",
    task: null,
    terminal: "w2:p1",
  }),
  agent({
    id: "m1.w1:p3",
    name: "refactor",
    observed_ms: NOW_MS - 2 * MINUTE_MS,
    report_ready: true,
    since_ms: NOW_MS - 10 * MINUTE_MS,
    started_ms: NOW_MS - HOUR_MS,
    state: "working",
    task: "Refactor the billing webhook handlers",
    terminal: "w1:p3",
  }),
  agent({
    developer: BRUNO,
    id: "m3.w1:p1",
    kind: "cli-b",
    machine: PC,
    model: "example/model-2",
    name: "deploy",
    observed_ms: NOW_MS - 5 * MINUTE_MS,
    project: "acme/api",
    role: "tasker",
    since_ms: null,
    started_ms: NOW_MS - 3 * HOUR_MS,
    state: "working",
    task: null,
  }),
  agent({
    id: "m1.w1:p4",
    name: "fix-login",
    observed_ms: NOW_MS - 3 * MINUTE_MS,
    report_ready: true,
    since_ms: NOW_MS - HOUR_MS,
    started_ms: NOW_MS - 5 * HOUR_MS,
    state: "done",
    task: "Fix the login redirect loop",
    terminal: "w1:p4",
  }),
  agent({
    developer: BRUNO,
    id: "m2.w1:p3",
    machine: LAPTOP_B,
    name: "archive",
    observed_ms: NOW_MS - 8 * MINUTE_MS,
    project: "acme/api",
    role: "tasker",
    since_ms: NOW_MS - 2 * HOUR_MS,
    started_ms: NOW_MS - 9 * HOUR_MS,
    state: "done",
    task: "Archive old invoices",
    terminal: "w1:p3",
  }),
  agent({
    id: "m3.w1:p2",
    kind: "cli-b",
    machine: PC,
    name: "watcher",
    observed_ms: NOW_MS - 12 * MINUTE_MS,
    role: "inspector",
    since_ms: NOW_MS - 45 * MINUTE_MS,
    started_ms: NOW_MS - 6 * HOUR_MS,
    state: "idle",
    task: "Watch the test suite",
    terminal: "w1:p2",
  }),
  agent({
    developer: BRUNO,
    id: "m2.w2:p1",
    kind: "cli-b",
    machine: LAPTOP_B,
    model: "example/model-2",
    name: "mystery",
    observed_ms: NOW_MS - 7 * MINUTE_MS,
    project: "acme/api",
    role: "reviewer",
    since_ms: NOW_MS - 90 * MINUTE_MS,
    started_ms: NOW_MS - 12 * HOUR_MS,
    state: "unknown",
    task: null,
    terminal: "w2:p1",
  }),
];

export const AGENTS_BY_ID = new Map(AGENTS.map((agent) => [agent.id, agent]));

export const AGENTS_RESPONSE: AgentsResponse = {
  generated_ms: NOW_MS,
  agents: AGENTS,
  hidden: [
    {
      counts: { done: 1, idle: 0, needs_you: 0, unknown: 0, working: 1 },
      machine: LAPTOP_B,
    },
    {
      counts: { done: 0, idle: 2, needs_you: 1, unknown: 0, working: 0 },
      machine: PC,
    },
  ],
};

export const AGENT_DETAIL_RESPONSES: Record<string, AgentDetailResponse> = {
  "m2.w1:p2": {
    agent: AGENTS_BY_ID.get("m2.w1:p2")!,
    history: [
      { at_ms: NOW_MS - 3 * MINUTE_MS, reason: "approval", state: "needs_you" },
      { at_ms: NOW_MS - 2 * HOUR_MS, reason: null, state: "working" },
    ],
  },
  "m1.w1:p4": {
    agent: AGENTS_BY_ID.get("m1.w1:p4")!,
    history: [],
  },
};

export const OVERVIEW_RESPONSE: OverviewResponse = {
  counts: { done: 3, idle: 3, needs_you: 3, unknown: 1, working: 3 },
  developers: 2,
  generated_ms: NOW_MS,
  machines: { offline: 1, online: 2 },
  needs_you: [AGENTS_BY_ID.get("m1.w2:p1")!, AGENTS_BY_ID.get("m2.w1:p2")!],
  per_machine: [
    {
      counts: { done: 1, idle: 0, needs_you: 1, unknown: 0, working: 1 },
      developer: ANA,
      machine: LAPTOP_A,
    },
    {
      counts: { done: 1, idle: 0, needs_you: 1, unknown: 1, working: 0 },
      developer: ANA,
      machine: LAPTOP_B,
    },
    {
      counts: { done: 0, idle: 1, needs_you: 0, unknown: 0, working: 1 },
      developer: ANA,
      machine: PC,
    },
  ],
  top_projects: [
    { agents: 4, project: "acme/web", working: 2 },
    { agents: 4, project: "acme/api", working: 1 },
  ],
  working_24h: Array.from({ length: 24 }, (_, index) => ({
    hour_ms: NOW_MS - (23 - index) * HOUR_MS,
    working: index === 22 ? 3 : index === 11 ? 2 : index === 5 ? 1 : 0,
  })),
};
