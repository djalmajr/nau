import { beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderHubApp, type HubApiState } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

/** Aguarda a Visão geral carregar. */
async function loadOverview() {
  await screen.findByText("Esperando por você agora");
  await screen.findByText("Agentes por máquina");
}

describe("overview", () => {
  it("shows the numbers, the waiting list, the machines, the 24 bars and the projects", async () => {
    renderHubApp("/");
    await loadOverview();

    // Cartões de número (ordem fixa): needs_you, working, done, idle,
    // máquinas online, desenvolvedores.
    const stats = screen.getAllByTestId("stat-value");
    expect(stats).toHaveLength(6);
    expect(stats[0]).toHaveTextContent("3");
    expect(stats[1]).toHaveTextContent("3");
    expect(stats[2]).toHaveTextContent("3");
    expect(stats[3]).toHaveTextContent("3");
    expect(stats[4]).toHaveTextContent("2 de 3");
    expect(stats[5]).toHaveTextContent("2");

    // Esperando por você: duas entradas, a mais antiga primeiro
    const waitingCard = screen
      .getByText("Esperando por você agora")
      .closest('[data-slot="card"]') as HTMLElement;
    const waitingLinks = within(waitingCard).getAllByRole("link");
    expect(waitingLinks).toHaveLength(2);
    expect(waitingLinks[0]).toHaveTextContent("scout");
    expect(waitingLinks[1]).toHaveTextContent("Retry declined cards in the checkout worker");

    // Agentes por máquina: três cartões; o pc offline é marcado
    const machines = screen.getByText("Agentes por máquina").closest("section") as HTMLElement;
    expect(within(machines).getByText("laptop-a")).toBeInTheDocument();
    expect(within(machines).getByText("laptop-b")).toBeInTheDocument();
    const pcCard = within(machines).getByText("pc").closest('[data-slot="card"]');
    expect(pcCard).toHaveTextContent("offline");

    // 24 barras com valor acessível em cada uma (o máximo é 3)
    const bars = screen.getAllByRole("img");
    expect(bars).toHaveLength(24);
    expect(bars.some((bar) => bar.getAttribute("aria-label")?.includes("3 trabalhando"))).toBe(
      true,
    );
    // Rótulo da hora no início, no meio e no fim
    expect(screen.getAllByText(/^\d{2}:\d{2}$/)).toHaveLength(3);
  });

  it("shows the top projects with agents and working counts", async () => {
    renderHubApp("/");
    await loadOverview();
    expect(screen.getByText("Projetos mais ativos")).toBeInTheDocument();
    const top = screen.getByText("acme/web").closest("li");
    expect(top).toHaveTextContent("4 agentes · 2 trabalhando");
  });

  it("shows the empty states when the hub has no data", async () => {
    const empty: HubApiState = {
      agentDetail: {},
      agents: null,
      calls: [],
      overview: {
        counts: { done: 0, idle: 0, needs_you: 0, unknown: 0, working: 0 },
        developers: 0,
        generated_ms: 0,
        machines: { offline: 0, online: 0 },
        needs_you: [],
        per_machine: [],
        top_projects: [],
        working_24h: Array.from({ length: 24 }, (_, index) => ({ hour_ms: index, working: 0 })),
      },
    };
    renderHubApp("/", empty);
    expect(await screen.findByText("Ninguém esperando por você")).toBeInTheDocument();
    expect(screen.getByText("Nenhuma máquina conectada.")).toBeInTheDocument();
    expect(screen.getByText("Nenhum projeto ativo.")).toBeInTheDocument();
  });

  it("shows the error state with retry when the request fails", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const state: HubApiState = {
      agentDetail: {},
      agents: null,
      calls: [],
      overview: null,
    };
    renderHubApp("/", state);
    expect(await screen.findByText("Algo deu errado. Tente novamente.")).toBeInTheDocument();
    const callsBefore = state.calls.filter((call) => call.url === "/api/overview").length;
    await user.click(screen.getByRole("button", { name: "Tentar de novo" }));
    await waitFor(() => {
      expect(state.calls.filter((call) => call.url === "/api/overview").length).toBeGreaterThan(
        callsBefore,
      );
    });
  });
});
