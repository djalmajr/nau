import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

describe("privacy", () => {
  it("shows the three modes with what each one delivers to the hub", async () => {
    renderHubApp("/privacy");

    expect(await screen.findByRole("heading", { name: "Os três modos" })).toBeInTheDocument();
    const modes = screen.getByRole("heading", { name: "Os três modos" }).parentElement!;
    expect(modes).toHaveTextContent("Exibir");
    expect(modes).toHaveTextContent("Oculto");
    expect(modes).toHaveTextContent("Não entregar");
    expect(modes).toHaveTextContent(
      "Os agentes do projeto aparecem no hub: projeto, estado, tarefa resumida e contagens.",
    );
  });

  it("explains where the choice lives, with the path as code", async () => {
    renderHubApp("/privacy");
    await screen.findByRole("heading", { name: "Onde a escolha mora" });
    const code = screen.getByText(".agents/soho.conf");
    expect(code.tagName).toBe("CODE");
    expect(code.closest("p")).toHaveTextContent("no diretório de cada projeto");
    expect(code.closest("p")).toHaveTextContent("O padrão é 'não entregar'.");
  });

  it("lists what never leaves the machine", async () => {
    renderHubApp("/privacy");
    const heading = await screen.findByRole("heading", { name: "O que nunca sai da máquina" });
    const list = heading.parentElement!;
    expect(list).toHaveTextContent("Brief");
    expect(list).toHaveTextContent("Relatório");
    expect(list).toHaveTextContent("Tela");
    expect(list).toHaveTextContent("Caminhos");
    expect(list).toHaveTextContent("Ambiente");
    expect(list).toHaveTextContent("Saída de comandos");
  });

  it("previews what the user's machine delivers: shown agents and hidden counts", async () => {
    renderHubApp("/privacy");
    await screen.findByRole("heading", { name: "Sua máquina agora" });

    // Ana (u1) tem agentes em m1, m2 e m3 (todos os agentes do fixture):
    // 8 exibidos; hidden de m2 (2) + m3 (3) = 5 ocultos.
    expect(screen.getByText("8 agentes em projetos exibidos")).toBeInTheDocument();
    expect(screen.getByText("5 em projetos ocultos")).toBeInTheDocument();
  });

  it("counts a hidden-only own machine in the preview", async () => {
    const state = defaultHubState();
    state.agents = {
      agents: [],
      generated_ms: Date.now(),
      hidden: [
        {
          counts: { done: 0, idle: 0, needs_you: 0, unknown: 0, working: 2 },
          machine: { id: "m2", name: "laptop-b", online: true },
        },
      ],
    };
    renderHubApp("/privacy", state);
    await screen.findByRole("heading", { name: "Sua máquina agora" });

    // A máquina é da usuária (GET /api/machines, developer = u1) mesmo sem
    // agentes exibidos: a prévia conta os ocultos dela e não mostra o
    // estado vazio.
    expect(screen.queryByText("Sua máquina não entrega agentes agora.")).not.toBeInTheDocument();
    expect(screen.getByText("2 em projetos ocultos")).toBeInTheDocument();
  });

  it("shows the empty preview when the machine delivers nothing", async () => {
    renderHubApp(
      "/privacy",
      defaultHubState({ agents: { agents: [], generated_ms: Date.now(), hidden: [] } }),
    );
    expect(await screen.findByText("Sua máquina não entrega agentes agora.")).toBeInTheDocument();
  });

  it("renders the error state with retry", async () => {
    renderHubApp("/privacy", defaultHubState({ agents: null }));
    expect(await screen.findByText("Algo deu errado. Tente novamente.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Tentar de novo" })).toBeInTheDocument();
  });
});
