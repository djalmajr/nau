// Harness dos testes de tela: router com as rotas reais + fetch falso que
// responde o contrato do hub sem rede.
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { parseAgentsSearch } from "@/lib/agents-search";
import type {
  AgentDetailResponse,
  AgentsResponse,
  DevelopersResponse,
  InvitesResponse,
  MachinesResponse,
  OverviewResponse,
  SessionUser,
  VersionResponse,
} from "@/lib/api";
import { I18nProvider } from "@/lib/i18n";
import { SESSION_QUERY_KEY, registerUnauthorizedRedirect } from "@/lib/session";
import { ThemeProvider } from "@/lib/theme";
import { AccessPage } from "@/routes/access";
import { AgentsPage } from "@/routes/agents";
import { AgentDetailPage } from "@/routes/agents-detail";
import { DevelopersPage } from "@/routes/developers";
import { InviteScreen } from "@/components/invite";
import { MachinesConnectPage } from "@/routes/machines-connect";
import { MachinesPage } from "@/routes/machines";
import { OverviewPage } from "@/routes/index";
import { PrivacyPage } from "@/routes/privacy";
import { ProjectsPage } from "@/routes/projects";
import { SettingsPage } from "@/routes/settings";
import {
  AGENT_DETAIL_RESPONSES,
  AGENTS_RESPONSE,
  DEVELOPERS_RESPONSE,
  INVITES_RESPONSE,
  MACHINES_RESPONSE,
  OVERVIEW_RESPONSE,
  VERSION_RESPONSE,
} from "./fixtures";

export const TEST_USER: SessionUser = {
  email: "ana@example.com",
  id: "u1",
  name: "Ana Exemplo",
  role: "owner",
};

export const TEST_MEMBER: SessionUser = {
  email: "bruno@example.com",
  id: "u2",
  name: "Bruno Exemplo",
  role: "member",
};

/** E-mail que o POST de compartilhamento aceita; qualquer outro vira 404
 *  (o contrato não distingue "máquina não sua" de "e-mail sem conta"). */
export const SHAREABLE_EMAIL = "bruno@example.com";

export interface HubApiState {
  /** null = erro 500 nos endpoints de dados. */
  agents: AgentsResponse | null;
  agentDetail: Record<string, AgentDetailResponse>;
  /** Status do endpoint de detalhe quando definido (ex.: 500). */
  agentDetailFail?: number;
  overview: OverviewResponse | null;
  /** null = erro 500 em GET /api/machines. */
  machines: MachinesResponse | null;
  /** Status do DELETE /api/machines/{id} quando definido (ex.: 404). */
  machineDeleteFail?: number;
  /** Código que o POST /api/pairings/confirm aceita (o servidor normaliza
   *  hífen/caixa); `pairingFail` (ex.: 400, 429) tem precedência. */
  pairingCode: string;
  pairingFail?: number;
  /** Usuário da sessão (GET /api/session). */
  sessionUser: SessionUser;
  /** Status do POST /api/session/password quando definido (ex.: 401, 400). */
  passwordFail?: number;
  developers: DevelopersResponse | null;
  invites: InvitesResponse;
  /** E-mail do POST /api/invites: `createInviteFail` (ex.: 403) tem
   *  precedência; o token é `tok-n`. */
  createInviteFail?: number;
  /** Status do DELETE /api/invites/{id} quando definido. */
  revokeInviteFail?: number;
  version: VersionResponse | null;
  /** Comportamento do POST /api/invites/accept: `inviteAcceptFail` (ex.:
   *  400, 409) tem precedência; senão 201 com `sessionUser`. */
  inviteAcceptFail?: number;
  calls: Array<{ method: string; url: string; body?: string }>;
}

export function defaultHubState(
  overrides: Partial<Omit<HubApiState, "calls">> = {},
): HubApiState {
  // Clone profundo: o harness MUTA o estado (criar convite, revogar, share…)
  // e cada teste precisa de um estado novo — senão as mutações vazam entre
  // os testes que compartilham os fixtures.
  const base = structuredClone({
    agentDetail: AGENT_DETAIL_RESPONSES,
    agents: AGENTS_RESPONSE,
    createInviteFail: undefined,
    developers: DEVELOPERS_RESPONSE,
    inviteAcceptFail: undefined,
    invites: INVITES_RESPONSE,
    machineDeleteFail: undefined,
    machines: MACHINES_RESPONSE,
    overview: OVERVIEW_RESPONSE,
    pairingCode: "abcd-efgh",
    pairingFail: undefined,
    passwordFail: undefined,
    revokeInviteFail: undefined,
    sessionUser: TEST_USER,
    version: VERSION_RESPONSE,
  });
  return {
    ...base,
    calls: [],
    ...overrides,
  };
}

export function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status,
  });
}

function noContent(status = 204) {
  return new Response(null, { status });
}

/** fetch falso do hub: sessão do usuário escolhido; dados conforme o estado.
 *  As escritas (share, unshare, invites, machine delete) mutam o estado,
 *  como o servidor faria, para a invalidação das queries ver o novo dado. */
export function fakeHubFetch(state: HubApiState) {
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = String(input);
    const url = raw.split("?")[0];
    const method = init?.method ?? "GET";
    const body = typeof init?.body === "string" ? init.body : undefined;
    state.calls.push({ body, method, url: raw });

    if (url === "/api/session") {
      if (method === "DELETE") return noContent();
      if (method === "POST") return jsonResponse(200, { user: state.sessionUser });
      return jsonResponse(200, { user: state.sessionUser });
    }
    if (url === "/api/session/password") {
      if (state.passwordFail !== undefined) {
        return jsonResponse(state.passwordFail, {
          error: state.passwordFail === 400 ? "weak_password" : "invalid_credentials",
        });
      }
      return noContent();
    }
    if (url === "/api/agents") {
      return state.agents ? jsonResponse(200, state.agents) : jsonResponse(500, { error: "boom" });
    }
    const agentMatch = url.match(/^\/api\/agents\/(.+)$/);
    if (agentMatch) {
      if (state.agentDetailFail !== undefined) {
        return jsonResponse(state.agentDetailFail, { error: "fixture_failure" });
      }
      const detail = state.agentDetail[decodeURIComponent(agentMatch[1])];
      return detail ? jsonResponse(200, detail) : jsonResponse(404, { error: "not_found" });
    }
    if (url === "/api/overview") {
      return state.overview ? jsonResponse(200, state.overview) : jsonResponse(500, { error: "boom" });
    }
    if (url === "/api/machines") {
      return state.machines ? jsonResponse(200, state.machines) : jsonResponse(500, { error: "boom" });
    }
    const machineDeleteMatch = url.match(/^\/api\/machines\/([^/]+)$/);
    if (machineDeleteMatch && method === "DELETE") {
      if (state.machineDeleteFail !== undefined) {
        return jsonResponse(state.machineDeleteFail, { error: "not_found" });
      }
      const machine = state.machines?.machines.find((m) => m.id === decodeURIComponent(machineDeleteMatch[1]));
      if (machine) machine.revoked = true;
      return noContent();
    }
    const shareMatch = url.match(/^\/api\/machines\/([^/]+)\/shares(?:\/(.+))?$/);
    if (shareMatch) {
      const machineId = decodeURIComponent(shareMatch[1]);
      const developerId = shareMatch[2] ? decodeURIComponent(shareMatch[2]) : null;
      const machine = state.machines?.machines.find((m) => m.id === machineId);
      if (!machine) return jsonResponse(404, { error: "not_found" });
      if (method === "POST") {
        const parsed = JSON.parse(body ?? "{}") as { email?: string };
        if (parsed.email !== SHAREABLE_EMAIL) return jsonResponse(404, { error: "not_found" });
        const developer = state.developers?.developers.find((d) => d.email === parsed.email);
        if (developer && !machine.shared_with.some((d) => d.id === developer.id)) {
          machine.shared_with.push({ id: developer.id, name: developer.name });
        }
        return jsonResponse(201, { developer: { id: "u2", name: "Bruno Exemplo" } });
      }
      if (method === "DELETE" && developerId) {
        machine.shared_with = machine.shared_with.filter((d) => d.id !== developerId);
        return noContent();
      }
      return jsonResponse(404, { error: "not_found" });
    }
    if (url === "/api/pairings/confirm") {
      if (state.pairingFail !== undefined) {
        return jsonResponse(state.pairingFail, {
          error: state.pairingFail === 429 ? "rate_limited" : "invalid_code",
        });
      }
      const parsed = JSON.parse(body ?? "{}") as { code?: string };
      const code = (parsed.code ?? "").replace(/-/g, "").toLowerCase();
      const valid = state.pairingCode.replace(/-/g, "").toLowerCase();
      return code === valid ? noContent() : jsonResponse(400, { error: "invalid_code" });
    }
    if (url === "/api/developers") {
      return state.developers ? jsonResponse(200, state.developers) : jsonResponse(500, { error: "boom" });
    }
    if (url === "/api/invites") {
      if (method === "POST") {
        if (state.createInviteFail !== undefined) {
          return jsonResponse(state.createInviteFail, {
            error: state.createInviteFail === 403 ? "forbidden" : "boom",
          });
        }
        const parsed = JSON.parse(body ?? "{}") as { email?: string };
        const invite = {
          email: parsed.email ?? "unknown@example.com",
          expires_ms: Date.now() + 7 * 24 * 60 * 60 * 1000,
          id: `inv-${state.invites.invites.length + 1}`,
          token: `tok-${state.invites.invites.length + 1}`,
        };
        state.invites.invites.unshift({ email: invite.email, expires_ms: invite.expires_ms, id: invite.id });
        return jsonResponse(201, invite);
      }
      return jsonResponse(200, state.invites);
    }
    const inviteDeleteMatch = url.match(/^\/api\/invites\/(.+)$/);
    if (inviteDeleteMatch && method === "DELETE") {
      if (state.revokeInviteFail !== undefined) {
        return jsonResponse(state.revokeInviteFail, { error: "boom" });
      }
      const id = decodeURIComponent(inviteDeleteMatch[1]);
      state.invites.invites = state.invites.invites.filter((invite) => invite.id !== id);
      return noContent();
    }
    if (url === "/api/invites/accept") {
      if (state.inviteAcceptFail !== undefined) {
        return jsonResponse(state.inviteAcceptFail, {
          error: state.inviteAcceptFail === 409 ? "email_taken" : "invalid_invite",
        });
      }
      // Contrato (docs/hub-api.md): senha fora de 12 a 128 bytes responde
      // 400 {"error":"weak_password"} — o servidor aplica a regra também
      // no aceite de convite.
      let acceptBody: { password?: unknown } = {};
      try {
        acceptBody = JSON.parse(body ?? "{}") as { password?: unknown };
      } catch {
        acceptBody = {};
      }
      const passwordBytes = new TextEncoder().encode(String(acceptBody.password ?? "")).length;
      if (passwordBytes < 12 || passwordBytes > 128) {
        return jsonResponse(400, { error: "weak_password" });
      }
      return jsonResponse(201, { user: state.sessionUser });
    }
    if (url === "/api/version") {
      return state.version ? jsonResponse(200, state.version) : jsonResponse(500, { error: "boom" });
    }
    return jsonResponse(404, { error: "not_found" });
  };
}

export interface HubApp {
  /** Estrutura mínima que os testes usam (evita o tipo completo do router). */
  router: { state: { location: { pathname: string; search: Record<string, unknown> } } };
  queryClient: QueryClient;
}

/** Renderiza as telas reais com providers completos e o fetch do hub falso.
 *  `initial` é a entrada da memory history (com query string, se quiser). */
export function renderHubApp(initial: string, state?: HubApiState): HubApp {
  const apiState = state ?? defaultHubState();
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false },
    },
  });
  const rootRoute = createRootRoute({
    component: () => (
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ThemeProvider>
            <TooltipProvider delay={0}>
              <Outlet />
            </TooltipProvider>
          </ThemeProvider>
        </I18nProvider>
      </QueryClientProvider>
    ),
  });
  // Mesmos caminhos e validateSearch das rotas do app — os hooks ligados
  // (`Route.useSearch` etc.) resolvem por id de rota no router ativo.
  const indexRoute = createRoute({ component: OverviewPage, getParentRoute: () => rootRoute, path: "/" });
  const agentsRoute = createRoute({
    component: AgentsPage,
    getParentRoute: () => rootRoute,
    path: "/agents",
    validateSearch: parseAgentsSearch,
  });
  const agentDetailRoute = createRoute({
    component: AgentDetailPage,
    getParentRoute: () => rootRoute,
    path: "/agents/$agentId",
    validateSearch: parseAgentsSearch,
  });
  const machinesRoute = createRoute({ component: MachinesPage, getParentRoute: () => rootRoute, path: "/machines" });
  const machinesConnectRoute = createRoute({
    component: MachinesConnectPage,
    getParentRoute: () => rootRoute,
    path: "/machines/connect",
  });
  const developersRoute = createRoute({
    component: DevelopersPage,
    getParentRoute: () => rootRoute,
    path: "/developers",
  });
  const projectsRoute = createRoute({ component: ProjectsPage, getParentRoute: () => rootRoute, path: "/projects" });
  const privacyRoute = createRoute({ component: PrivacyPage, getParentRoute: () => rootRoute, path: "/privacy" });
  const accessRoute = createRoute({ component: AccessPage, getParentRoute: () => rootRoute, path: "/access" });
  const settingsRoute = createRoute({ component: SettingsPage, getParentRoute: () => rootRoute, path: "/settings" });
  const inviteRoute = createRoute({ component: InviteScreen, getParentRoute: () => rootRoute, path: "/invite" });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [initial] }),
    routeTree: rootRoute.addChildren([
      indexRoute,
      agentsRoute,
      agentDetailRoute,
      machinesRoute,
      machinesConnectRoute,
      developersRoute,
      projectsRoute,
      privacyRoute,
      accessRoute,
      settingsRoute,
      inviteRoute,
    ]),
  });
  registerUnauthorizedRedirect(queryClient, router);
  vi.stubGlobal("fetch", fakeHubFetch(apiState));
  // Sessão já verificada (caso quente, como na navegação normal do app):
  // evita a corrida sessão→dados no teste e não afeta os estados de erro
  // das telas de dados, que é o alvo da suíte.
  queryClient.setQueryData(SESSION_QUERY_KEY, apiState.sessionUser);
  render(<RouterProvider router={router} />);
  return { router, queryClient };
}
