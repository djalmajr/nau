// Harness dos testes de tela: router com as rotas reais (Visão geral,
// Agentes, Detalhe) + fetch falso que responde o contrato do hub sem rede.
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
  OverviewResponse,
  SessionUser,
} from "@/lib/api";
import { I18nProvider } from "@/lib/i18n";
import { SESSION_QUERY_KEY, registerUnauthorizedRedirect } from "@/lib/session";
import { ThemeProvider } from "@/lib/theme";
import { AgentsPage } from "@/routes/agents";
import { AgentDetailPage } from "@/routes/agents-detail";
import { OverviewPage } from "@/routes/index";
import { AGENT_DETAIL_RESPONSES, AGENTS_RESPONSE, OVERVIEW_RESPONSE } from "./fixtures";

export const TEST_USER: SessionUser = {
  email: "ana@example.com",
  id: "u1",
  name: "Ana Exemplo",
  role: "owner",
};

export interface HubApiState {
  /** null = erro 500 nos endpoints de dados. */
  agents: AgentsResponse | null;
  agentDetail: Record<string, AgentDetailResponse>;
  /** Status do endpoint de detalhe quando definido (ex.: 500). */
  agentDetailFail?: number;
  overview: OverviewResponse | null;
  calls: Array<{ method: string; url: string }>;
}

export function defaultHubState(
  overrides: Partial<Omit<HubApiState, "calls">> = {},
): HubApiState {
  return {
    agentDetail: AGENT_DETAIL_RESPONSES,
    agents: AGENTS_RESPONSE,
    calls: [],
    overview: OVERVIEW_RESPONSE,
    ...overrides,
  };
}

export function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status,
  });
}

/** fetch falso do hub: sessão sempre logada; dados conforme o estado. */
export function fakeHubFetch(state: HubApiState) {
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = String(input);
    const url = raw.split("?")[0];
    const method = init?.method ?? "GET";
    state.calls.push({ method, url: raw });
    if (url === "/api/session") return jsonResponse(200, { user: TEST_USER });
    if (url === "/api/agents") {
      return state.agents ? jsonResponse(200, state.agents) : jsonResponse(500, { error: "boom" });
    }
    const match = url.match(/^\/api\/agents\/(.+)$/);
    if (match) {
      if (state.agentDetailFail !== undefined) {
        return jsonResponse(state.agentDetailFail, { error: "fixture_failure" });
      }
      const detail = state.agentDetail[decodeURIComponent(match[1])];
      return detail ? jsonResponse(200, detail) : jsonResponse(404, { error: "not_found" });
    }
    if (url === "/api/overview") {
      return state.overview ? jsonResponse(200, state.overview) : jsonResponse(500, { error: "boom" });
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
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [initial] }),
    routeTree: rootRoute.addChildren([indexRoute, agentsRoute, agentDetailRoute]),
  });
  registerUnauthorizedRedirect(queryClient, router);
  vi.stubGlobal("fetch", fakeHubFetch(apiState));
  // Sessão já verificada (caso quente, como na navegação normal do app):
  // evita a corrida sessão→dados no teste e não afeta os estados de erro
  // das telas de dados, que é o alvo da suíte.
  queryClient.setQueryData(SESSION_QUERY_KEY, TEST_USER);
  render(<RouterProvider router={router} />);
  return { router, queryClient };
}
