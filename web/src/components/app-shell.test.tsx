import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { AppShell } from "@/components/app-shell";
import { TooltipProvider } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import { I18nProvider, LOCALE_STORAGE_KEY } from "@/lib/i18n";
import { registerUnauthorizedRedirect, RequireSession, SESSION_QUERY_KEY } from "@/lib/session";
import { ThemeProvider } from "@/lib/theme";

const USER = { id: "u1", name: "Ana Exemplo", email: "ana@example.com", role: "owner" as const };

type FakeSessionState = { calls: Array<{ method: string; url: string }>; loggedIn: boolean };

/** fetch falso da API de sessão com estado em memória: GET 200 enquanto
 *  `loggedIn`, 401 depois (DELETE revoga). */
function fakeSessionFetch(state: FakeSessionState) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    state.calls.push({ method, url });
    if (url !== "/api/session") {
      return new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
    }
    if (method === "GET") {
      return state.loggedIn
        ? new Response(JSON.stringify({ user: USER }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          })
        : new Response(JSON.stringify({ error: "unauthenticated" }), {
            status: 401,
            headers: { "Content-Type": "application/json" },
          });
    }
    if (method === "DELETE") {
      state.loggedIn = false;
      return new Response(null, { status: 204 });
    }
    return new Response(JSON.stringify({ error: "invalid_credentials" }), { status: 401 });
  });
}

/** Casca real dentro de um router mínimo ("/" + "/agents" protegida +
 *  "/login") com os mesmos providers do __root.tsx. O conteúdo é filho da
 *  casca; o tratamento central de 401 é o mesmo do main.tsx. */
function renderShell(initial = "/") {
  const queryClient = new QueryClient({
    // staleTime igual ao do main.tsx: a reentrada no teste acontece dentro da
    // janela em que a query ainda contaria como fresca.
    defaultOptions: { queries: { retry: false, staleTime: 10_000 } },
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
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => (
      <AppShell>
        <div data-testid="content">content</div>
      </AppShell>
    ),
  });
  const agentsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/agents",
    component: () => (
      <RequireSession>
        <AppShell>
          <div data-testid="agents-content">agents</div>
        </AppShell>
      </RequireSession>
    ),
  });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/login",
    component: () => <div data-testid="login-screen">login</div>,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, agentsRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  registerUnauthorizedRedirect(queryClient, router);
  render(<RouterProvider router={router} />);
  return { router, queryClient };
}

const PT_ITEMS = ["Visão geral", "Agentes", "Máquinas", "Desenvolvedores", "Projetos", "Privacidade", "Acesso", "Configurações"];
const EN_ITEMS = ["Overview", "Agents", "Machines", "Developers", "Projects", "Privacy", "Access", "Settings"];

beforeEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("AppShell sidebar", () => {
  it("shows the brand, three groups and eight items in pt-BR", async () => {
    vi.spyOn(api, "getSession").mockResolvedValue({ user: USER });
    renderShell();

    // Marca no topo da sidebar.
    expect(await screen.findByText("Nau")).toBeInTheDocument();
    // Três grupos (o rótulo "Projetos" coincide com o item; usa getAllByText).
    for (const group of ["Visão", "Projetos", "Sistema"]) {
      expect(screen.getAllByText(group).length).toBeGreaterThanOrEqual(1);
    }
    // Oito itens (o item "Projetos" também é rótulo de grupo; o título ativo
    // se repete no header, então usa getAllByText).
    for (const item of PT_ITEMS) {
      expect(screen.getAllByText(item).length).toBeGreaterThanOrEqual(1);
    }
  });

  it("shows the groups and items in en-US after switching the language", async () => {
    vi.spyOn(api, "getSession").mockResolvedValue({ user: USER });
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    renderShell();

    expect((await screen.findAllByText("Visão geral")).length).toBeGreaterThanOrEqual(1);

    // Troca o idioma pelo seletor do header.
    await user.click(screen.getByRole("button", { name: "Idioma" }));
    await user.click(await screen.findByRole("menuitemradio", { name: "English" }));

    await waitFor(() => {
      for (const group of ["Views", "Projects", "System"]) {
        expect(screen.getAllByText(group).length).toBeGreaterThanOrEqual(1);
      }
      for (const item of EN_ITEMS) {
        expect(screen.getAllByText(item).length).toBeGreaterThanOrEqual(1);
      }
    });
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("en-US");
  });

  it("localizes the sidebar trigger label and follows the language switch", async () => {
    vi.spyOn(api, "getSession").mockResolvedValue({ user: USER });
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    renderShell();

    // pt-BR (padrão): o nome acessível do gatilho passa pelo dicionário.
    expect(
      await screen.findByRole("button", { name: "Mostrar ou ocultar a barra lateral" }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Idioma" }));
    await user.click(await screen.findByRole("menuitemradio", { name: "English" }));

    expect(await screen.findByRole("button", { name: "Toggle Sidebar" })).toBeInTheDocument();
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("en-US");
  });
});

describe("AppShell user menu", () => {
  it("signs out: removes the session from the cache and the next protected route redirects to /login", async () => {
    const session: FakeSessionState = { calls: [], loggedIn: true };
    vi.stubGlobal("fetch", fakeSessionFetch(session));

    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router, queryClient } = renderShell();

    // O nome vem da sessão.
    expect(await screen.findByRole("button", { name: "Conta" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Conta" }));
    expect(screen.getByText("Ana Exemplo")).toBeInTheDocument();

    await user.click(await screen.findByText("Sair"));

    await waitFor(() => {
      expect(session.calls.some((c) => c.method === "DELETE" && c.url === "/api/session")).toBe(true);
    });
    expect(screen.getByTestId("login-screen")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");

    // A query da sessão sumiu do QueryClient.
    expect(queryClient.getQueryData(SESSION_QUERY_KEY)).toBeUndefined();

    // Ainda dentro do staleTime, reentrar numa rota protegida redireciona
    // para /login (o refetch responde 401: a sessão não está no cache).
    const getCalls = () => session.calls.filter((c) => c.method === "GET").length;
    const getCallsBefore = getCalls();
    router.navigate({ to: "/agents" });
    await waitFor(
      () => {
        // O GET novo (401) já ocorreu E a location voltou para /login; as duas
        // condições juntas impedem o assert de passar antes da ida e volta.
        expect(getCalls()).toBeGreaterThan(getCallsBefore);
        expect(router.state.location.pathname).toBe("/login");
      },
      { timeout: 3000 },
    );
    expect(screen.queryByTestId("agents-content")).not.toBeInTheDocument();
    expect(screen.getByTestId("login-screen")).toBeInTheDocument();
    expect(queryClient.getQueryData(SESSION_QUERY_KEY)).toBeUndefined();
  });
});
