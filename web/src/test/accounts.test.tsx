// Troca de conta: os dados de uma conta nunca podem aparecer em outra.
// Adaptado do probe da revisão (review-4-20261030T173104.accounts.probe.tsx):
// u1 vê os seus agentes, sai (o logout limpa o QueryClient inteiro), u2 entra
// e só os agentes do u2 aparecem, com requisição nova.
import { render, screen } from "@testing-library/react";
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
import { expect, it, vi } from "vitest";
import { LoginScreen } from "@/components/login";
import { TooltipProvider } from "@/components/ui/tooltip";
import { parseAgentsSearch } from "@/lib/agents-search";
import { setUnauthorizedHandler } from "@/lib/api";
import { I18nProvider } from "@/lib/i18n";
import { AGENTS_QUERY_KEY } from "@/lib/queries";
import { registerUnauthorizedRedirect } from "@/lib/session";
import { ThemeProvider } from "@/lib/theme";
import { AgentsPage } from "@/routes/agents";
import { OverviewPage } from "@/routes/index";
import { AGENTS, OVERVIEW_RESPONSE } from "./fixtures";

const OLD_TASK = "Previous account task";

it("clears u1's data on logout and only shows u2's agents after login", async () => {
  localStorage.clear();
  let account = "u1";
  let logged = true;
  const calls: string[] = [];
  const oldAgent = { ...AGENTS[0], task: OLD_TASK };
  const users = {
    u1: { id: "u1", name: "Ana Exemplo", email: "ana@example.com", role: "owner" as const },
    u2: { id: "u2", name: "Bruno Exemplo", email: "bruno@example.com", role: "member" as const },
  };

  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url === "/api/session") {
        if (method === "DELETE") {
          logged = false;
          return new Response(null, { status: 204 });
        }
        if (method === "POST") {
          account = "u2";
          logged = true;
        }
        return Response.json(
          logged ? { user: users[account as keyof typeof users] } : { error: "unauthenticated" },
          { status: logged ? 200 : 401 },
        );
      }
      if (url === "/api/agents") {
        calls.push(account);
        return Response.json({
          agents: account === "u1" ? [oldAgent] : [],
          generated_ms: 0,
          hidden: [],
        });
      }
      if (url === "/api/overview") return Response.json({ ...OVERVIEW_RESPONSE, needs_you: [] });
      return Response.json({ error: "not_found" }, { status: 404 });
    },
  );

  // Mesma staleTime de produção (10 s): sem a limpeza, o cache fresco de u1
  // ainda valeria quando u2 abre Agentes.
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 10_000 },
      mutations: { retry: false },
    },
  });
  const root = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
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
  const agents = createRoute({
    component: AgentsPage,
    getParentRoute: () => root,
    path: "/agents",
    validateSearch: parseAgentsSearch,
  });
  const index = createRoute({ component: OverviewPage, getParentRoute: () => root, path: "/" });
  const login = createRoute({
    component: LoginScreen,
    getParentRoute: () => root,
    path: "/login",
    validateSearch: (s: Record<string, unknown>) => ({
      next: typeof s.next === "string" ? s.next : undefined,
    }),
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/agents"] }),
    routeTree: root.addChildren([agents, index, login]),
  });
  registerUnauthorizedRedirect(client, router);

  render(<RouterProvider router={router} />);
  const user = userEvent.setup({ pointerEventsCheck: 0 });

  // u1 vê o seu agente.
  await screen.findByText(OLD_TASK);

  // Logout: o QueryClient inteiro é limpo — os dados de u1 saem do cache
  // antes de u2 sequer entrar. O handler central de 401 fica desregistrado
  // nesta fase para isolar a limpeza do próprio logout (o handler tem a sua
  // própria limpeza e é coberto pelos testes da base de sessão).
  setUnauthorizedHandler(null);
  await user.click(screen.getByRole("button", { name: "Conta" }));
  await user.click(await screen.findByRole("menuitem", { name: "Sair" }));
  expect(await screen.findByLabelText("E-mail")).toBeInTheDocument();
  expect(client.getQueryData(AGENTS_QUERY_KEY("u1"))).toBeUndefined();
  registerUnauthorizedRedirect(client, router);

  // u2 entra: só os agentes dela, com requisição nova.
  await user.type(screen.getByLabelText("E-mail"), "bruno@example.com");
  await user.type(screen.getByLabelText("Senha"), "fixture");
  await user.click(screen.getByRole("button", { name: "Entrar" }));
  await screen.findByText("Esperando por você agora");
  await user.click(screen.getByRole("link", { name: "Agentes" }));
  await screen.findByRole("combobox", { name: "Desenvolvedor" });

  expect(screen.queryByText(OLD_TASK)).not.toBeInTheDocument();
  // Uma carga para u1 e uma nova para u2 — nenhuma outra.
  expect(calls).toEqual(["u1", "u2"]);
});
