import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

/** Aguarda a Visão geral carregar. */
async function loadOverview() {
  await screen.findByText("Esperando por você agora");
  await screen.findByText("Agentes por máquina");
}

describe("overview", () => {
  it("shows the numbers, the waiting list, the machines, the working chart and the projects", async () => {
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
    // "Precisa de você" acima de zero destaca o número em text-primary
    expect(stats[0].className).toContain("text-primary");

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

    // Gráfico das últimas 24 h: o ChartContainer entra no lugar (o SVG das
    // barras depende de layout e não aparece no jsdom) e a lista acessível
    // tem 24 itens, com o valor máximo (3)
    const working = screen.getByRole("region", { name: "Trabalhando nas últimas 24 h" });
    expect(working.querySelector('[data-slot="chart"]')).not.toBeNull();
    const items = within(working).getAllByRole("listitem");
    expect(items).toHaveLength(24);
    expect(items.some((item) => item.textContent?.includes("3 trabalhando"))).toBe(true);
  });

  it("draws the chart from the data: one bar per hour above zero, in order, with the tooltip", async () => {
    // jsdom não tem layout: o gráfico só desenha com uma medida real do contêiner.
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      bottom: 160,
      height: 160,
      left: 0,
      right: 800,
      toJSON: () => ({}),
      top: 0,
      width: 800,
      x: 0,
      y: 0,
    });
    renderHubApp("/");
    await loadOverview();

    const working = screen.getByRole("region", { name: "Trabalhando nas últimas 24 h" });
    const bars = await waitFor(() => {
      // Do fixture: 1 na hora 5, 2 na hora 11 e 3 na hora 22; as demais em zero.
      const found = Array.from(working.querySelectorAll(".recharts-bar-rectangle path"));
      expect(found).toHaveLength(3);
      return found;
    });
    const heights = bars.map((bar) => Number(bar.getAttribute("height")));
    const positions = bars.map((bar) => Number(bar.getAttribute("x")));
    expect(heights[0]).toBeGreaterThan(0);
    expect(heights[1] / heights[0]).toBeCloseTo(2);
    expect(heights[2] / heights[0]).toBeCloseTo(3);
    expect(positions[0]).toBeLessThan(positions[1]);
    expect(positions[1]).toBeLessThan(positions[2]);

    // A barra mais alta mostra o valor dela no tooltip (o texto igual da lista
    // acessível não conta: a busca fica só no tooltip).
    const chart = working.querySelector(".recharts-wrapper") as HTMLElement;
    fireEvent.mouseMove(chart, { clientX: positions[2] + 1, clientY: 80 });
    await waitFor(() => {
      expect(working.querySelector(".recharts-tooltip-wrapper")).toHaveTextContent("3 trabalhando");
    });
  });

  it("shows the top projects with agents and working counts", async () => {
    renderHubApp("/");
    await loadOverview();
    expect(screen.getByText("Projetos mais ativos")).toBeInTheDocument();
    const top = screen.getByText("acme/web").closest("li");
    expect(top).toHaveTextContent("4 agentes · 2 trabalhando");
  });

  it("shows the empty states when the hub has no data", async () => {
    const empty = defaultHubState({
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
    });
    renderHubApp("/", empty);
    expect(await screen.findByText("Ninguém esperando por você")).toBeInTheDocument();
    expect(screen.getByText("Nenhuma máquina conectada.")).toBeInTheDocument();
    expect(screen.getByText("Nenhum projeto ativo.")).toBeInTheDocument();
    // "Precisa de você" em zero: o número fica sem o destaque text-primary
    const stats = screen.getAllByTestId("stat-value");
    expect(stats[0].className).not.toContain("text-primary");
    // Nenhuma hora trabalhando: mensagem vazia no lugar do gráfico
    expect(screen.getByText("Nenhum agente trabalhando nas últimas 24 h.")).toBeInTheDocument();
    const working = screen.getByRole("region", { name: "Trabalhando nas últimas 24 h" });
    expect(working.querySelector('[data-slot="chart"]')).toBeNull();
    expect(within(working).queryAllByRole("listitem")).toHaveLength(0);
  });

  it("shows the error state with retry when the request fails", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const state = defaultHubState({ overview: null });
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
