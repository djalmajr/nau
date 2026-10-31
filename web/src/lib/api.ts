// Thin client for the hub session API. Same-origin: in dev, Vite proxies /api
// to the hub (default port 7440); in production the hub serves the built SPA.
const BASE = "/api";
/** Teto de tempo por requisição (JSON, mesma origem). Nenhum endpoint é
 *  streaming/SSE, então abortar é seguro e evita spinner travado num hub lento. */
const REQUEST_TIMEOUT_MS = 20000;

/** Error thrown by the session API client, carrying the HTTP status so the UI
 *  can map it to a human, localized message instead of surfacing raw text. */
export class ApiError extends Error {
  readonly status: number;
  readonly serverError: string;
  constructor(status: number, serverError = "") {
    super(serverError ? `${status}: ${serverError}` : String(status));
    this.name = "ApiError";
    this.status = status;
    this.serverError = serverError;
  }
}

/** true para aborts vindos de AbortSignal.timeout("TimeoutError") ou AbortController ("AbortError"). */
function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && (err.name === "TimeoutError" || err.name === "AbortError");
}

/** Opções das chamadas de dados: o `signal` vem da camada TanStack Query
 *  (cancelamento no unmount/clear) e o `user` é a conta dona da query (a id
 *  que entra na chave): o 401 central ignora respostas de uma conta que já
 *  não é a sessão vigente. */
export interface RequestOptions {
  signal?: AbortSignal;
  user?: string;
}

/** true quando o erro é um 401 da API: a sessão expirou/foi revogada. */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401;
}

type UnauthorizedHandler = (requestingUser?: string) => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

/** Tratamento central de 401 de chamadas autenticadas. O app registra um
 *  único handler (ver `registerUnauthorizedRedirect` em lib/session), que
 *  recebe a conta dona da requisição quando a query a conhece: assim um 401
 *  atrasado de uma conta anterior não derruba a conta que acabou de entrar.
 *  O 401 do POST de login é excluído em `req`: ali é credencial errada, não
 *  sessão expirada, e a mensagem fica na própria tela. */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler;
}

/** Resultado do emparelhamento manual do signal: o signal para o fetch e o
 *  `dispose` que remove o timer do timeout e o listener no signal do
 *  chamador. Quem compõe roda o `dispose` no `finally` da requisição,
 *  depois da leitura do corpo, para sucesso, erro, timeout e cancelamento
 *  não deixarem timer pendente nem listener registrado. */
interface RequestSignalHandle {
  signal: AbortSignal;
  dispose: () => void;
}

/** O signal que o fetch usa: o do chamador (TanStack Query) combinado com o
 *  timeout do cliente — o cancelamento do chamador propaga até o fetch
 *  (o `clear()` do logout fecha as requisições em voo) e o timeout continua
 *  virando ApiError(408). O emparelhamento é manual (sem `AbortSignal.any`:
 *  o ambiente jsdom dos testes não expõe `any`); o timer do timeout é
 *  `unref`-ado como o de `AbortSignal.timeout`, para não segurar o processo.
 */
function requestSignal(caller: AbortSignal | null | undefined): RequestSignalHandle {
  if (!caller) {
    // Sem chamador: o timer é do próprio AbortSignal.timeout, sem listener
    // nosso para remover.
    return { dispose: () => {}, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) };
  }
  const controller = new AbortController();
  if (caller.aborted) {
    controller.abort(caller.reason);
    return { dispose: () => {}, signal: controller.signal };
  }
  let timer: ReturnType<typeof setTimeout> | undefined = setTimeout(() => {
    timer = undefined;
    controller.abort(new DOMException("Timed out", "TimeoutError"));
  }, REQUEST_TIMEOUT_MS);
  (timer as { unref?: () => void }).unref?.();
  const onCallerAbort = () => {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
    controller.abort(caller.reason);
  };
  caller.addEventListener("abort", onCallerAbort, { once: true });
  return {
    dispose: () => {
      if (timer !== undefined) {
        clearTimeout(timer);
        timer = undefined;
      }
      caller.removeEventListener("abort", onCallerAbort);
    },
    signal: controller.signal,
  };
}

async function req<T>(path: string, init?: RequestInit, user?: string): Promise<T> {
  const { dispose, signal } = requestSignal(init?.signal);
  try {
    let res: Response;
    try {
      res = await fetch(BASE + path, {
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        ...init,
        signal,
      });
    } catch (err) {
      // Cancelado pelo chamador (ex.: o clear() do logout/401): o erro de
      // abort sobe como está e a camada TanStack Query trata o cancelamento;
      // a UI não confunde isso com timeout.
      if (init?.signal?.aborted) throw err;
      // Timeout/abort sem chamador → ApiError(408) para virar estado de
      // erro/retry na UI; erros de rede genuínos (TypeError) sobem inalterados.
      if (isAbortError(err)) throw new ApiError(408);
      throw err;
    }
    if (!res.ok) {
      let serverError = "";
      try {
        const body = (await res.json()) as { error?: unknown };
        if (typeof body?.error === "string") serverError = body.error;
      } catch {
        // non-JSON error body
      }
      // 401 numa chamada autenticada significa sessão perdida: o handler
      // central remove a sessão do cache e leva o usuário de volta ao login.
      // Duas respostas são a exceção (o 401 ali é erro de formulário, não
      // sessão expirada): o POST de login (credencial errada) e a senha atual
      // errada no POST de troca de senha — e só ela: o contrato responde
      // `invalid_credentials` nesse caso, enquanto `unauthenticated` é a
      // sessão expirada/revogada e passa pelo handler central como qualquer
      // outra rota.
      const method = String(init?.method ?? "GET").toUpperCase();
      const isLogin = path === "/session" && method === "POST";
      const isWrongPassword = path === "/session/password" && serverError === "invalid_credentials";
      if (res.status === 401 && !isLogin && !isWrongPassword) unauthorizedHandler?.(user);
      throw new ApiError(res.status, serverError);
    }
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  } finally {
    // Depois da leitura do corpo (ou da falha): nenhum caminho deixa timer
    // pendente nem listener no signal do chamador.
    dispose();
  }
}

export type SessionRole = "owner" | "member";

export interface SessionUser {
  id: string;
  name: string;
  email: string;
  role: SessionRole;
}

export interface SessionResponse {
  user: SessionUser;
}

// Tipos do contrato de leitura do hub (docs/hub-api.md). Nomes e formatos
// espelham o JSON do contrato; o servidor aplica a regra de permissão — a UI
// nunca filtra por permissão.

export type AgentState = "needs_you" | "working" | "done" | "idle" | "unknown";

export type AgentReason =
  | "question"
  | "approval"
  | "quota"
  | "provider"
  | "not_received"
  | "no_report"
  | "compact"
  | "owner";

export interface Machine {
  id: string;
  name: string;
  online: boolean;
}

export interface Developer {
  id: string;
  name: string;
}

/** Contagens sempre com as cinco chaves (projetos ocultos e totais). */
export interface Counts {
  working: number;
  needs_you: number;
  done: number;
  idle: number;
  unknown: number;
}

/** Um agente vivo, como o último inventário da máquina o reportou. */
export interface Agent {
  id: string;
  machine: Machine;
  developer: Developer;
  project: string;
  session: string;
  terminal: string;
  name: string;
  role: string;
  kind: string;
  model: string;
  effort: string;
  state: AgentState;
  reason: AgentReason | null;
  task: string | null;
  report_ready: boolean;
  started_ms: number;
  since_ms: number | null;
  observed_ms: number;
}

/** Projeto oculto: o hub expõe a máquina e as contagens, não os agentes. */
export interface HiddenCounts {
  machine: Machine;
  counts: Counts;
}

export interface AgentsResponse {
  generated_ms: number;
  agents: Agent[];
  hidden: HiddenCounts[];
}

export interface AgentHistoryEntry {
  state: AgentState;
  reason: AgentReason | null;
  at_ms: number;
}

export interface AgentDetailResponse {
  agent: Agent;
  history: AgentHistoryEntry[];
}

export interface OverviewPerMachine {
  machine: Machine;
  developer: Developer;
  counts: Counts;
}

export interface WorkingHour {
  hour_ms: number;
  working: number;
}

export interface TopProject {
  project: string;
  agents: number;
  working: number;
}

export interface OverviewResponse {
  generated_ms: number;
  counts: Counts;
  machines: { online: number; offline: number };
  developers: number;
  needs_you: Agent[];
  per_machine: OverviewPerMachine[];
  working_24h: WorkingHour[];
  top_projects: TopProject[];
}

/** Máquina com os dados da chave (GET /api/machines): as próprias e as
 *  compartilhadas. `shared_with` vem preenchido só nas próprias. */
export interface HubMachine {
  id: string;
  name: string;
  developer: Developer;
  created_ms: number;
  revoked: boolean;
  last_seen_ms: number | null;
  online: boolean;
  shared_with: Developer[];
}

export interface MachinesResponse {
  machines: HubMachine[];
}

/** Desenvolvedor com as contagens de contabilidade (GET /api/developers). */
export interface HubDeveloper {
  id: string;
  name: string;
  email: string;
  machines: number;
  /** Máquinas que este desenvolvedor compartilha com o usuário. */
  shares_with_me: number;
  /** Máquinas que o usuário compartilha com este desenvolvedor. */
  shared_by_me: number;
}

export interface DevelopersResponse {
  developers: HubDeveloper[];
}

/** Convite pendente (GET/POST /api/invites). O `token` só existe na resposta
 *  do POST: aparece uma única vez. */
export interface Invite {
  id: string;
  email: string;
  expires_ms: number;
}

export interface InvitesResponse {
  invites: Invite[];
}

export interface InviteCreated extends Invite {
  token: string;
}

/** Versão do hub (GET /api/version). */
export interface VersionResponse {
  version: string;
}

/** Versão do quadro da sessão (GET /api/live): os 32 caracteres que mudam
 *  quando o que o usuário pode ver muda. */
export interface LiveVersionResponse {
  version: string;
}

export const api = {
  /** GET /api/session — 200 { user } ou 401 { error: "unauthenticated" }. */
  getSession: (signal?: AbortSignal) => req<SessionResponse>("/session", { signal }),
  /** POST /api/session — 200 { user } (+cookie HttpOnly), 401 invalid_credentials, 429 rate_limited. */
  login: (email: string, password: string) =>
    req<SessionResponse>("/session", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    }),
  /** DELETE /api/session — 204. */
  logout: () => req<void>("/session", { method: "DELETE" }),
  /** GET /api/agents — 200 { generated_ms, agents, hidden }. A UI filtra a
   *  lista completa no cliente (opção prevista no contrato); os parâmetros
   *  opcionais `developer`, `machine` e `project` ficam no servidor. */
  getAgents: (options: RequestOptions = {}) =>
    req<AgentsResponse>("/agents", { signal: options.signal }, options.user),
  /** GET /api/agents/{id} — 200 { agent, history } ou 404 { error: "not_found" }. */
  getAgent: (id: string, options: RequestOptions = {}) =>
    req<AgentDetailResponse>(`/agents/${encodeURIComponent(id)}`, { signal: options.signal }, options.user),
  /** GET /api/overview — 200 com contagens, máquinas, espera, 24 h e projetos. */
  getOverview: (options: RequestOptions = {}) =>
    req<OverviewResponse>("/overview", { signal: options.signal }, options.user),
  /** GET /api/machines — 200 { machines }; as próprias + as compartilhadas. */
  getMachines: (options: RequestOptions = {}) =>
    req<MachinesResponse>("/machines", { signal: options.signal }, options.user),
  /** DELETE /api/machines/{id} — 204 revoga a chave (só o dono; 404 caso contrário). */
  revokeMachine: (id: string, options: RequestOptions = {}) =>
    req<void>(`/machines/${encodeURIComponent(id)}`, { method: "DELETE", signal: options.signal }, options.user),
  /** POST /api/pairings/confirm — 204 vincula o pareamento; 400 código inválido/expirado;
   *  429 depois de 10 códigos errados em 15 min. O código pode vir com ou sem
   *  hífen e em qualquer caixa (o servidor normaliza). */
  confirmPairing: (code: string, options: RequestOptions = {}) =>
    req<void>("/pairings/confirm", {
      body: JSON.stringify({ code }),
      method: "POST",
      signal: options.signal,
    }, options.user),
  /** POST /api/machines/{id}/shares — 201 { developer }; 404 máquina não própria
   *  ou e-mail sem conta (os dois casos se parecem). */
  shareMachine: (id: string, email: string, options: RequestOptions = {}) =>
    req<{ developer: Developer }>(`/machines/${encodeURIComponent(id)}/shares`, {
      body: JSON.stringify({ email }),
      method: "POST",
      signal: options.signal,
    }, options.user),
  /** DELETE /api/machines/{id}/shares/{developerId} — 204 encerra o compartilhamento. */
  unshareMachine: (id: string, developerId: string, options: RequestOptions = {}) =>
    req<void>(`/machines/${encodeURIComponent(id)}/shares/${encodeURIComponent(developerId)}`, {
      method: "DELETE",
      signal: options.signal,
    }, options.user),
  /** GET /api/developers — 200 { developers }. */
  getDevelopers: (options: RequestOptions = {}) =>
    req<DevelopersResponse>("/developers", { signal: options.signal }, options.user),
  /** POST /api/invites — 201 { id, email, expires_ms, token } (owner; 403 membro).
   *  O token aparece só nesta resposta. */
  createInvite: (email: string, options: RequestOptions = {}) =>
    req<InviteCreated>("/invites", {
      body: JSON.stringify({ email, role: "member" }),
      method: "POST",
      signal: options.signal,
    }, options.user),
  /** GET /api/invites — 200 { invites } (só os pendentes). */
  getInvites: (options: RequestOptions = {}) =>
    req<InvitesResponse>("/invites", { signal: options.signal }, options.user),
  /** DELETE /api/invites/{id} — 204. */
  revokeInvite: (id: string, options: RequestOptions = {}) =>
    req<void>(`/invites/${encodeURIComponent(id)}`, { method: "DELETE", signal: options.signal }, options.user),
  /** POST /api/invites/accept — sem sessão: 201 { user } (+cookie),
   *  400 convite inválido/expirado, 409 e-mail já usado. */
  acceptInvite: (token: string, name: string, password: string) =>
    req<SessionResponse>("/invites/accept", {
      body: JSON.stringify({ name, password, token }),
      method: "POST",
    }),
  /** POST /api/session/password — 204 (nova cookie; demais sessões terminam);
   *  401 senha atual errada; 400 weak_password. */
  changePassword: (current: string, newPassword: string, options: RequestOptions = {}) =>
    req<void>("/session/password", {
      body: JSON.stringify({ current, new: newPassword }),
      method: "POST",
      signal: options.signal,
    }, options.user),
  /** GET /api/version — 200 { version }. */
  getVersion: (options: RequestOptions = {}) =>
    req<VersionResponse>("/version", { signal: options.signal }, options.user),
  /** GET /api/live — 200 { version } (a versão do quadro da sessão) ou 401
   *  sessão perdida (passa pelo tratamento central de 401). A recaída do
   *  canal ao vivo consulta aqui quando o stream não abre. */
  getLiveVersion: (options: RequestOptions = {}) =>
    req<LiveVersionResponse>("/live", { signal: options.signal }, options.user),
};
