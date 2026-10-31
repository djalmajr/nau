import { beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AGENTS, AGENTS_RESPONSE } from "@/test/fixtures";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

/** Aguarda o Board carregar (quatro colunas). */
async function loadBoard() {
  await screen.findByRole("region", { name: "Precisa de você" });
  await screen.findByRole("region", { name: "Trabalhando" });
  await screen.findByRole("region", { name: "Concluído" });
  await screen.findByRole("region", { name: "Ocioso" });
}

describe("agents board", () => {
  it("puts each agent in the column of its state, oldest first", async () => {
    renderHubApp("/agents");
    await loadBoard();

    // needs_you: scout (30 min) é mais antigo que build (3 min)
    const needsYou = screen.getByRole("region", { name: "Precisa de você" });
    expect(within(needsYou).getByTestId("column-count")).toHaveTextContent("2");
    const needsYouCards = within(needsYou).getAllByRole("link");
    expect(needsYouCards).toHaveLength(2);
    expect(needsYouCards[0]).toHaveTextContent("scout");
    expect(needsYouCards[1]).toHaveTextContent("Retry declined cards in the checkout worker");

    // working: refactor (10 min) antes de deploy (obs 5 min, since null)
    const working = screen.getByRole("region", { name: "Trabalhando" });
    expect(within(working).getByTestId("column-count")).toHaveTextContent("2");
    const workingCards = within(working).getAllByRole("link");
    expect(workingCards[0]).toHaveTextContent("Refactor the billing webhook handlers");
    expect(workingCards[1]).toHaveTextContent("deploy");

    // done: archive (2 h) antes de fix-login (1 h)
    const done = screen.getByRole("region", { name: "Concluído" });
    const doneCards = within(done).getAllByRole("link");
    expect(doneCards[0]).toHaveTextContent("Archive old invoices");
    expect(doneCards[1]).toHaveTextContent("Fix the login redirect loop");

    // idle: watcher
    const idle = screen.getByRole("region", { name: "Ocioso" });
    const idleCards = within(idle).getAllByRole("link");
    expect(idleCards).toHaveLength(1);
    expect(idleCards[0]).toHaveTextContent("Watch the test suite");
  });

  it("dims the card of an agent on an offline machine and marks it", async () => {
    renderHubApp("/agents");
    await loadBoard();
    const working = screen.getByRole("region", { name: "Trabalhando" });
    const deployCard = within(working)
      .getAllByRole("link")
      .find((link) => link.textContent?.includes("deploy"));
    expect(deployCard).toBeDefined();
    expect(deployCard).toHaveClass("opacity-60");
    expect(deployCard).toHaveTextContent("offline");
  });

  it("shows the hidden card per column only when the per-state sum is above zero", async () => {
    renderHubApp("/agents");
    await loadBoard();

    // hidden: laptop-b {working 1, done 1} + pc {needs_you 1, idle 2}
    expect(
      within(screen.getByRole("region", { name: "Precisa de você" })).getByText("1 em projetos ocultos"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("region", { name: "Trabalhando" })).getByText("1 em projetos ocultos"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("region", { name: "Concluído" })).getByText("1 em projetos ocultos"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("region", { name: "Ocioso" })).getByText("2 em projetos ocultos"),
    ).toBeInTheDocument();

    // O cartão de ocultos não é link.
    const needsYou = screen.getByRole("region", { name: "Precisa de você" });
    const hiddenCard = within(needsYou).getByText("1 em projetos ocultos");
    expect(hiddenCard.closest("a")).toBeNull();
  });

  it("shows the unknown band only when there are unknown agents", async () => {
    renderHubApp("/agents");
    await loadBoard();
    const band = screen.getByTestId("unknown-band");
    expect(within(band).getByTestId("unknown-count")).toHaveTextContent("1");
    expect(within(band).getAllByRole("link")[0]).toHaveTextContent("mystery");
  });

  it("hides the unknown band when no agent is in that state", async () => {
    const state = defaultHubState({
      agents: {
        ...AGENTS_RESPONSE,
        agents: AGENTS.filter((agent) => agent.state !== "unknown"),
      },
    });
    renderHubApp("/agents", state);
    await loadBoard();
    expect(screen.queryByTestId("unknown-band")).not.toBeInTheDocument();
  });
});

describe("agents list", () => {
  it("shows the same rows in the same order as the board", async () => {
    renderHubApp("/agents?view=list");
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row");
    // 1 cabeçalho + 8 agentes + 1 rodapé de ocultos
    expect(rows).toHaveLength(10);
    const names = rows.slice(1, 9).map((row) => row.textContent);
    expect(names).toEqual([
      expect.stringContaining("scout"),
      expect.stringContaining("build"),
      expect.stringContaining("refactor"),
      expect.stringContaining("deploy"),
      expect.stringContaining("archive"),
      expect.stringContaining("fix-login"),
      expect.stringContaining("watcher"),
      expect.stringContaining("mystery"),
    ]);
    expect(rows[9]).toHaveTextContent("5 agentes em projetos ocultos");
  });

  it("marks offline machines in the machine cell", async () => {
    renderHubApp("/agents?view=list");
    const table = await screen.findByRole("table");
    const deployRow = within(table)
      .getAllByRole("row")
      .find((row) => row.textContent?.includes("deploy"));
    expect(deployRow).toHaveTextContent("offline");
  });
});

describe("view toggle and filters", () => {
  it("switching between Board and List changes the URL and the content", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp("/agents");
    await loadBoard();

    await user.click(screen.getByRole("tab", { name: "Lista" }));
    expect(router.state.location.search).toMatchObject({ view: "list" });
    expect(screen.getByRole("table")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Board" }));
    expect(router.state.location.search).toMatchObject({ view: "board" });
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Precisa de você" })).toBeInTheDocument();
  });

  it("the developer filter changes the URL and keeps only that developer's agents", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp("/agents");
    await loadBoard();

    await user.click(screen.getByRole("combobox", { name: "Desenvolvedor" }));
    await user.click(await screen.findByRole("option", { name: "Bruno Exemplo" }));

    expect(router.state.location.search).toMatchObject({ developer: "u2" });
    const cardCount = screen
      .getAllByTestId("board-column")
      .reduce((sum, region) => sum + Number(within(region).getByTestId("column-count").textContent), 0);
    // Bruno nas colunas: scout, deploy e archive; mystery fica na faixa
    // desconhecida (fora das quatro colunas).
    expect(cardCount).toBe(3);
    expect(screen.getByRole("region", { name: "Precisa de você" })).toHaveTextContent("scout");
    expect(screen.getByTestId("unknown-band")).toHaveTextContent("mystery");
  });

  it("the machine filter hides hidden cards from other machines", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp("/agents");
    await loadBoard();

    await user.click(screen.getByRole("combobox", { name: "Máquina" }));
    await user.click(await screen.findByRole("option", { name: "laptop-a" }));

    expect(router.state.location.search).toMatchObject({ machine: "m1" });
    expect(screen.queryByText("em projetos ocultos")).not.toBeInTheDocument();
    // Só os agentes da laptop-a: scout, refactor e fix-login
    const cardCount = screen
      .getAllByTestId("board-column")
      .reduce((sum, region) => sum + Number(within(region).getByTestId("column-count").textContent), 0);
    expect(cardCount).toBe(3);
  });

  it("the search filters by task or agent name via the q param", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp("/agents");
    await loadBoard();

    const searchInput = screen.getByRole("searchbox", { name: "Buscar por tarefa ou nome do agente" });
    await user.type(searchInput, "checkout");

    await waitFor(() => {
      expect(router.state.location.search).toMatchObject({ q: "checkout" });
    });
    const cardCount = screen
      .getAllByTestId("board-column")
      .reduce((sum, region) => sum + Number(within(region).getByTestId("column-count").textContent), 0);
    expect(cardCount).toBe(1);
    expect(screen.getByRole("region", { name: "Precisa de você" })).toHaveTextContent(
      "Retry declined cards in the checkout worker",
    );
  });

  it("the project filter and All reset it back to no filter", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp("/agents");
    await loadBoard();

    await user.click(screen.getByRole("combobox", { name: "Projeto" }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
    expect(router.state.location.search).toMatchObject({ project: "acme/api" });

    await user.click(screen.getByRole("combobox", { name: "Projeto" }));
    await user.click(await screen.findByRole("option", { name: "Todos" }));
    await waitFor(() => {
      expect(router.state.location.search).not.toMatchObject({ project: "acme/api" });
    });
  });
});

describe("invalid search params", () => {
  it("an invalid view falls back to the board", async () => {
    const { router } = renderHubApp("/agents?view=kanban");
    await loadBoard();
    expect(router.state.location.search).not.toMatchObject({ view: "kanban" });
    expect(screen.getByRole("region", { name: "Precisa de você" })).toBeInTheDocument();
  });

  it("a filter value that is not in the received list falls back to no filter", async () => {
    const { router } = renderHubApp("/agents?machine=nao-existe");
    await loadBoard();
    await waitFor(() => {
      expect(router.state.location.search).not.toMatchObject({ machine: "nao-existe" });
    });
    // Sem filtro: os ocultos voltam a aparecer (três somas de "1").
    expect(screen.getAllByText("1 em projetos ocultos")).toHaveLength(3);
  });
});

describe("agents empty and error states", () => {
  it("shows the empty state when the hub has no agents", async () => {
    const state = defaultHubState({
      agents: { generated_ms: 0, agents: [], hidden: [] },
    });
    renderHubApp("/agents", state);
    expect(await screen.findByText("Nenhum agente registrado no hub.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Precisa de você" })).not.toBeInTheDocument();
  });

  it("shows the filtered empty state when the filters match nothing", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const state = defaultHubState({
      agents: { generated_ms: 0, agents: [], hidden: [] },
    });
    renderHubApp("/agents?q=qualquer-coisa", state);
    expect(await screen.findByText("Nenhum agente com os filtros atuais.")).toBeInTheDocument();
    void user;
  });

  it("shows the error state with retry when the request fails", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const state = defaultHubState({ agents: null });
    renderHubApp("/agents", state);
    expect(await screen.findByText("Algo deu errado. Tente novamente.")).toBeInTheDocument();
    const callsBefore = state.calls.filter((call) => call.url.startsWith("/api/agents")).length;
    await user.click(screen.getByRole("button", { name: "Tentar de novo" }));
    await waitFor(() => {
      expect(state.calls.filter((call) => call.url.startsWith("/api/agents")).length).toBeGreaterThan(
        callsBefore,
      );
    });
  });
});

describe("agents cache keys", () => {
  it("opens a detail whose opaque id is `list` after loading the list", async () => {
    // O id é opaco pelo contrato: "list" é id válido e não pode colidir com a
    // chave da lista (a colisão fazia o detalhe renderizar a AgentsResponse).
    const a = { ...AGENTS[0], id: "list" };
    const state = defaultHubState({
      agentDetail: { list: { agent: a, history: [] } },
      agents: { agents: [a], generated_ms: 0, hidden: [] },
    });
    renderHubApp("/agents", state);
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const link = await screen.findByRole("link", { name: /Retry declined cards/ });
    await user.click(link);
    expect(
      await screen.findByRole("heading", { name: a.task ?? a.name }),
    ).toBeInTheDocument();
  });
});
