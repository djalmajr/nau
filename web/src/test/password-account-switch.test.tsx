import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { useChangePassword } from "@/lib/queries";
import { registerUnauthorizedRedirect, SESSION_QUERY_KEY } from "@/lib/session";
import { defaultHubState, renderHubApp, TEST_MEMBER, TEST_USER } from "@/test/hub-harness";

afterEach(() => vi.unstubAllGlobals());

// A troca de senha da conta A segue em voo; a sessão passa a ser de B; o
// 401 `unauthenticated` de A chega depois. Ele é da conta anterior: a sessão
// e os dados de B ficam, e ninguém vai para o login.
it("keeps the new account when a late password 401 from the old account arrives", async () => {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  client.setQueryData(SESSION_QUERY_KEY, TEST_USER);
  const moves: string[] = [];
  registerUnauthorizedRedirect(client, {
    navigate: (opts) => {
      moves.push(opts.to);
    },
    state: { location: { href: "/settings", pathname: "/settings" } },
  });
  let finish!: (response: Response) => void;
  let started = false;
  vi.stubGlobal("fetch", () => {
    started = true;
    return new Promise<Response>((resolve) => {
      finish = resolve;
    });
  });
  const hook = renderHook(() => useChangePassword(), {
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  });
  act(() => hook.result.current.mutate({ current: "old-password-123", newPassword: "new-password-123" }));
  await waitFor(() => expect(started).toBe(true));
  act(() => {
    client.clear();
    client.setQueryData(SESSION_QUERY_KEY, TEST_MEMBER);
    client.setQueryData(["developers", TEST_MEMBER.id], { marker: "new-account" });
  });
  await act(async () => finish(Response.json({ error: "unauthenticated" }, { status: 401 })));
  await waitFor(() => expect(hook.result.current.isError).toBe(true));
  expect(client.getQueryData(SESSION_QUERY_KEY)).toEqual(TEST_MEMBER);
  expect(client.getQueryData(["developers", TEST_MEMBER.id])).toEqual({ marker: "new-account" });
  expect(moves).toEqual([]);
});

// Uma máquina só compartilhada, que entrega só projetos ocultos, não entra
// na prévia das máquinas próprias da Privacidade.
it("leaves a shared machine's hidden counts out of the own privacy preview", async () => {
  localStorage.clear();
  const state = defaultHubState();
  const shared = state.machines!.machines.find((m) => m.developer.id !== TEST_USER.id)!;
  state.agents = {
    agents: [],
    generated_ms: Date.now(),
    hidden: [
      {
        counts: { done: 0, idle: 0, needs_you: 0, unknown: 0, working: 9 },
        machine: { id: shared.id, name: shared.name, online: true },
      },
    ],
  };
  renderHubApp("/privacy", state);
  expect(await screen.findByText("Sua máquina não entrega agentes agora.")).toBeInTheDocument();
  expect(screen.queryByText("9 em projetos ocultos")).not.toBeInTheDocument();
});
