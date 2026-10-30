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
import { LoginScreen } from "@/components/login";
import { registerUnauthorizedRedirect, RequireSession, safeNext, SESSION_QUERY_KEY, useSession } from "@/lib/session";
import { I18nProvider, LOCALE_STORAGE_KEY } from "@/lib/i18n";
import { ThemeProvider } from "@/lib/theme";
import { TooltipProvider } from "@/components/ui/tooltip";

const USER = { id: "u1", name: "Ana Exemplo", email: "ana@example.com", role: "owner" as const };

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type SessionApi = {
  calls: Array<{ method: string; url: string }>;
  /** false = 401; true = 200 com o user (estado pós-login). */
  loggedIn: boolean;
};

/** fetch falso para a API de sessão: sem rede, estado em memória. */
function fakeSessionFetch(apiState: SessionApi, post: () => Response | Promise<Response>) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    apiState.calls.push({ method, url });
    if (url !== "/api/session") return jsonResponse(404, { error: "not_found" });
    if (method === "GET") {
      return apiState.loggedIn
        ? jsonResponse(200, { user: USER })
        : jsonResponse(401, { error: "unauthenticated" });
    }
    if (method === "POST") return post();
    if (method === "DELETE") {
      apiState.loggedIn = false;
      return new Response(null, { status: 204 });
    }
    return jsonResponse(405, { error: "method_not_allowed" });
  });
}

/** Conteúdo protegido mostra o nome da sessão: deixa os testes checarem que
 *  o nome antigo não fica na tela depois do 401. */
function AgentsContent() {
  const { data } = useSession();
  return (
    <div data-testid="agents-screen">
      <span>agents</span>
      <span data-testid="session-name">{data?.name ?? ""}</span>
    </div>
  );
}

/** Router com "/", "/agents" (protegida) e "/login" (tela real de login). */
function renderApp(initial: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
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
    component: () => <div>home</div>,
  });
  const agentsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/agents",
    component: () => (
      <RequireSession>
        <AgentsContent />
      </RequireSession>
    ),
  });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/login",
    validateSearch: (search: Record<string, unknown>): { next?: string } => ({
      next: typeof search.next === "string" ? search.next : undefined,
    }),
    component: LoginScreen,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, agentsRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  // Tratamento central de 401 (idêntico ao do main.tsx).
  registerUnauthorizedRedirect(queryClient, router);
  render(<RouterProvider router={router} />);
  return { router, queryClient };
}

async function submitLogin(user: ReturnType<typeof userEvent.setup>, email: string, password: string) {
  // O router renderiza de forma assíncrona no jsdom: aguarde a tela antes de digitar.
  await user.type(await screen.findByLabelText("E-mail"), email);
  await user.type(screen.getByLabelText("Senha"), password);
  await user.click(screen.getByRole("button", { name: "Entrar" }));
}

beforeEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("session guard", () => {
  it("redirects a protected route to /login?next=… without a session", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    vi.stubGlobal("fetch", fakeSessionFetch(apiState, () => jsonResponse(401, { error: "invalid_credentials" })));

    const { router } = renderApp("/agents");

    expect(await screen.findByLabelText("E-mail")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
    expect(router.state.location.search.next).toBe("/agents");
    expect(apiState.calls.some((c) => c.method === "GET" && c.url === "/api/session")).toBe(true);
  });

  it("drops the cached session and goes to login when a refetch answers 401", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: true };
    vi.stubGlobal(
      "fetch",
      fakeSessionFetch(apiState, () => jsonResponse(401, { error: "invalid_credentials" })),
    );

    const { router, queryClient } = renderApp("/agents");

    // 200: a rota protegida aparece com o nome da sessão no conteúdo.
    expect(await screen.findByText("agents")).toBeInTheDocument();
    expect(screen.getByTestId("session-name")).toHaveTextContent("Ana Exemplo");

    // A sessão expira: o refetch responde 401 e o handler central remove a
    // query do cache e navega para o login.
    apiState.loggedIn = false;
    await queryClient.refetchQueries({ queryKey: SESSION_QUERY_KEY });

    expect(await screen.findByLabelText("E-mail")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
    // O nome antigo não fica na tela: a query saiu do cache.
    expect(screen.queryByText("Ana Exemplo")).not.toBeInTheDocument();
    expect(queryClient.getQueryData(SESSION_QUERY_KEY)).toBeUndefined();
  });
});

describe("login", () => {
  it("goes to `next` after a 200", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    const fetchMock = fakeSessionFetch(apiState, () => {
      apiState.loggedIn = true;
      return jsonResponse(200, { user: USER });
    });
    vi.stubGlobal("fetch", fetchMock);

    const { router } = renderApp("/login?next=%2Fagents");
    const user = userEvent.setup();
    await submitLogin(user, "ana@example.com", "senha-123");

    expect(await screen.findByText("agents")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/agents");
  });

  it("sends the credentials in the POST body only", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    const fetchMock = fakeSessionFetch(apiState, () => {
      apiState.loggedIn = true;
      return jsonResponse(200, { user: USER });
    });
    vi.stubGlobal("fetch", fetchMock);

    renderApp("/login");
    const user = userEvent.setup();
    await submitLogin(user, "ana@example.com", "senha-123");

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/session",
        expect.objectContaining({
          method: "POST",
          credentials: "same-origin",
          body: JSON.stringify({ email: "ana@example.com", password: "senha-123" }),
        }),
      );
    });
  });

  it("falls back to / for a protocol-relative next", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    vi.stubGlobal(
      "fetch",
      fakeSessionFetch(apiState, () => {
        apiState.loggedIn = true;
        return jsonResponse(200, { user: USER });
      }),
    );

    const { router } = renderApp("/login?next=%2F%2Foutro");
    const user = userEvent.setup();
    await submitLogin(user, "ana@example.com", "senha-123");

    expect(await screen.findByText("home")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/");
  });

  it("falls back to / for an absolute-URL next", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    vi.stubGlobal(
      "fetch",
      fakeSessionFetch(apiState, () => {
        apiState.loggedIn = true;
        return jsonResponse(200, { user: USER });
      }),
    );

    const { router } = renderApp("/login?next=https%3A%2F%2Foutro.example");
    const user = userEvent.setup();
    await submitLogin(user, "ana@example.com", "senha-123");

    expect(await screen.findByText("home")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/");
  });

  it("shows the 401 message for invalid credentials", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    vi.stubGlobal(
      "fetch",
      fakeSessionFetch(apiState, () => jsonResponse(401, { error: "invalid_credentials" })),
    );

    renderApp("/login");
    const user = userEvent.setup();
    await submitLogin(user, "ana@example.com", "senha-errada");

    expect(await screen.findByRole("alert")).toHaveTextContent("E-mail ou senha incorretos");
    expect(screen.getByRole("button", { name: "Entrar" })).not.toBeDisabled();
  });

  it("shows the 429 message when rate limited", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    vi.stubGlobal(
      "fetch",
      fakeSessionFetch(apiState, () => jsonResponse(429, { error: "rate_limited" })),
    );

    renderApp("/login");
    const user = userEvent.setup();
    await submitLogin(user, "ana@example.com", "senha-123");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Muitas tentativas. Tente de novo em instantes.",
    );
  });

  it("keeps the button disabled while the request is in flight", async () => {
    const apiState: SessionApi = { calls: [], loggedIn: false };
    let resolveLogin: (res: Response) => void;
    const inFlight = new Promise<Response>((resolve) => {
      resolveLogin = resolve;
    });
    vi.stubGlobal("fetch", fakeSessionFetch(apiState, () => inFlight));

    renderApp("/login?next=%2Fagents");
    const user = userEvent.setup();

    await user.type(await screen.findByLabelText("E-mail"), "ana@example.com");
    await user.type(screen.getByLabelText("Senha"), "senha-123");
    const button = screen.getByRole("button", { name: "Entrar" });
    await user.click(button);
    // Envia: o botão desabilita enquanto a promessa está pendente.
    expect(button).toBeDisabled();
    expect(screen.queryByText("agents")).not.toBeInTheDocument();

    resolveLogin!(
      jsonResponse(200, { user: USER }),
    );
    apiState.loggedIn = true;

    // Aprovado: navega para `next` e a tela de login sai do DOM (o botão
    // anterior vira nó solto, então a checagem é no destino).
    expect(await screen.findByText("agents")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Entrar" })).not.toBeInTheDocument();
  });
});

describe("safeNext", () => {
  it("accepts internal paths and rejects external or empty values", () => {
    expect(safeNext("/agents")).toBe("/agents");
    expect(safeNext("/")).toBe("/");
    expect(safeNext("//outro")).toBe("/");
    expect(safeNext("https://outro.example")).toBe("/");
    expect(safeNext("")).toBe("/");
    expect(safeNext(undefined)).toBe("/");
  });

  it("rejects backslashes and control characters", () => {
    expect(safeNext("/\\x")).toBe("/"); // /\x
    expect(safeNext("/\tx")).toBe("/"); // / + tab + x
    expect(safeNext("/\u0000x")).toBe("/");
    expect(safeNext("/\u007Fx")).toBe("/");
  });

  it("keeps a percent-encoded internal path (same site)", () => {
    expect(safeNext("/%2F%2Fx")).toBe("/%2F%2Fx");
  });
});

describe("locale persistence across login", () => {
  it("keeps a stored locale on the login screen", async () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "en-US");
    const apiState: SessionApi = { calls: [], loggedIn: false };
    vi.stubGlobal(
      "fetch",
      fakeSessionFetch(apiState, () => jsonResponse(401, { error: "invalid_credentials" })),
    );

    renderApp("/login");

    expect(await screen.findByLabelText("Email")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });
});
