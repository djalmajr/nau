// Ciclo de vida de sessão e conta, adaptado do probe de revisão
// review-4-20261030T201532.lifecycle.probe.tsx: nenhuma consulta de dados
// antes da sessão, o 401 atrasado da conta anterior não derruba a conta que
// acabou de entrar (o request antigo é cancelado no clear() e o handler
// ignora 401 de usuário desatualizado) e o unknown oculto aparece no Board.
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { api, setUnauthorizedHandler, type SessionUser } from "@/lib/api";
import { SESSION_QUERY_KEY, registerUnauthorizedRedirect } from "@/lib/session";
import {
  AGENTS,
  AGENT_DETAIL_RESPONSES,
  AGENTS_RESPONSE,
  LAPTOP_A,
  OVERVIEW_RESPONSE,
} from "@/test/fixtures";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";
import { router as productionRouter } from "@/router";

const USERS: Record<"u1" | "u2", SessionUser> = {
  u1: { email: "ana@example.com", id: "u1", name: "Ana Exemplo", role: "owner" },
  u2: { email: "bruno@example.com", id: "u2", name: "Bruno Exemplo", role: "member" },
};

/** App de verdade (rotas de produção) com a fetch falsa do teste; a sessão
 *  só resolve quando o teste libera. */
function boot(path = "/agents") {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 10_000 },
      mutations: { retry: false },
    },
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree: productionRouter.routeTree,
  });
  registerUnauthorizedRedirect(client, router);
  render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>,
  );
  return { client, router };
}

beforeEach(() => {
  localStorage.clear();
  window.scrollTo = () => {};
});

afterEach(() => {
  setUnauthorizedHandler(null);
  vi.unstubAllGlobals();
});

it.each(["/agents", "/agents/m2.w1%3Ap2", "/"])(
  "does not fetch protected data before the session arrives: %s",
  async (path) => {
    let release!: (response: Response) => void;
    const session = new Promise<Response>((resolve) => {
      release = resolve;
    });
    let sessions = 0;
    const data: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/session") {
          sessions += 1;
          return session;
        }
        data.push(url);
        if (url === "/api/agents") return Response.json(AGENTS_RESPONSE);
        if (url === "/api/overview") return Response.json(OVERVIEW_RESPONSE);
        return Response.json(AGENT_DETAIL_RESPONSES["m2.w1:p2"]);
      }),
    );
    boot(path);
    await waitFor(() => expect(sessions).toBe(1));
    expect(data).toEqual([]);
    await act(async () => {
      release(Response.json({ user: USERS.u1 }));
    });
    await waitFor(() => expect(data.length).toBeGreaterThan(0));
  },
);

it("an old account 401 must not clear the newly logged-in account", async () => {
  let account: "u1" | "u2" = "u1";
  let logged = true;
  let hold = false;
  let release!: (response: Response) => void;
  let oldSignal: AbortSignal | undefined;
  const calls: string[] = [];
  const oldTask = "Previous account task";
  const newTask = "Current account task";
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url === "/api/session") {
        if (method === "DELETE") logged = false;
        if (method === "POST") {
          account = "u2";
          logged = true;
        }
        return Response.json(
          logged ? { user: USERS[account] } : { error: "unauthenticated" },
          { status: logged ? 200 : 401 },
        );
      }
      if (url === "/api/agents") {
        calls.push(account);
        if (account === "u1" && hold) {
          // O refresh em voo de u1: segura a resposta e registra o signal
          // que o cliente combinou (cancelamento + timeout).
          oldSignal = init?.signal ?? undefined;
          return new Promise<Response>((resolve, reject) => {
            release = resolve;
            const onAbort = () => reject(new DOMException("Aborted", "AbortError"));
            if (oldSignal?.aborted) onAbort();
            else oldSignal?.addEventListener("abort", onAbort, { once: true });
          });
        }
        return Response.json({
          agents: [{ ...AGENTS[0], task: account === "u1" ? oldTask : newTask }],
          generated_ms: 0,
          hidden: [],
        });
      }
      if (url === "/api/overview") return Response.json({ ...OVERVIEW_RESPONSE, needs_you: [] });
      return Response.json({ error: "not_found" }, { status: 404 });
    }),
  );
  const { client, router } = boot();
  const user = userEvent.setup({ pointerEventsCheck: 0 });
  await screen.findByText(oldTask);
  hold = true;
  const pending = client.refetchQueries({ queryKey: ["agents", "list", "u1"] });
  await waitFor(() => expect(calls).toHaveLength(2));
  await user.click(screen.getByRole("button", { name: "Conta" }));
  await user.click(await screen.findByRole("menuitem", { name: "Sair" }));
  await user.type(await screen.findByLabelText("E-mail"), "bruno@example.com");
  await user.type(screen.getByLabelText("Senha"), "fixture");
  await user.click(screen.getByRole("button", { name: "Entrar" }));
  await screen.findByText("Esperando por você agora");
  await user.click(screen.getByRole("link", { name: "Agentes" }));
  await screen.findByText(newTask);
  const aborted = oldSignal?.aborted ?? false;
  await act(async () => {
    release(Response.json({ error: "unauthenticated" }, { status: 401 }));
    await pending;
    await new Promise((resolve) => setTimeout(resolve, 100));
  });
  // O clear() do logout cancelou o refresh em voo (o signal chegou ao
  // fetch); e o 401 atrasado, se resolvesse, é de u1 e não derruba u2.
  expect(aborted).toBe(true);
  expect(calls).toEqual(["u1", "u1", "u2"]);
  expect(router.state.location.pathname).toBe("/agents");
  expect(client.getQueryData<{ id: string }>(SESSION_QUERY_KEY)?.id).toBe("u2");
  expect(screen.getByText(newTask)).toBeInTheDocument();
  client.clear();
});

it("the central 401 handler ignores a stale user's response even when not aborted", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(SESSION_QUERY_KEY, USERS.u2);
  const navigations: unknown[] = [];
  const router = {
    navigate: (options: { to: string }) => {
      navigations.push(options);
    },
    state: { location: { href: "/agents", pathname: "/agents" } },
  };
  registerUnauthorizedRedirect(client, router);
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ error: "unauthenticated" }, { status: 401 })));
  // Requisição de u1 (a conta anterior) que não foi abortada: o handler
  // compara a dona da requisição com a sessão vigente (u2) e ignora.
  await expect(api.getAgents({ user: "u1" })).rejects.toThrow();
  expect(navigations).toHaveLength(0);
  expect(client.getQueryData<{ id: string }>(SESSION_QUERY_KEY)?.id).toBe("u2");
});

it("the central 401 handler still clears and navigates for the current user", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(SESSION_QUERY_KEY, USERS.u2);
  const navigations: unknown[] = [];
  const router = {
    navigate: (options: { to: string }) => {
      navigations.push(options);
    },
    state: { location: { href: "/agents", pathname: "/agents" } },
  };
  registerUnauthorizedRedirect(client, router);
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ error: "unauthenticated" }, { status: 401 })));
  // 401 da conta vigente: sessão expirada de verdade — limpa e vai ao login.
  await expect(api.getAgents({ user: "u2" })).rejects.toThrow();
  expect(navigations).toHaveLength(1);
  expect(client.getQueryData(SESSION_QUERY_KEY)).toBeUndefined();
});

it("keeps a hidden unknown agent visible when switching Board and List", async () => {
  const state = defaultHubState({
    agents: {
      agents: [],
      generated_ms: 0,
      hidden: [
        {
          counts: { done: 0, idle: 0, needs_you: 0, unknown: 1, working: 0 },
          machine: LAPTOP_A,
        },
      ],
    },
  });
  renderHubApp("/agents", state);
  await screen.findByRole("combobox", { name: "Desenvolvedor" });
  const boardVisible = !!screen.queryByText("1 em projetos ocultos");
  const band = !!screen.queryByTestId("unknown-band");
  await userEvent.setup({ pointerEventsCheck: 0 }).click(screen.getByRole("tab", { name: "Lista" }));
  await screen.findByText("1 agentes em projetos ocultos");
  // O Board sem agente unknown visível mostra a faixa com a contagem oculta
  // (a Lista já mostrava o total oculto; o Board omitia o unknown).
  expect(boardVisible).toBe(true);
  expect(band).toBe(true);
});
