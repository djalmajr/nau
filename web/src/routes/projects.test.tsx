import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";
import { AGENTS_RESPONSE } from "@/test/fixtures";

beforeEach(() => {
  localStorage.clear();
});

describe("projects", () => {
  it("groups shown projects with agent, needs_you and working counts", async () => {
    const user = userEvent.setup();
    const { router } = renderHubApp("/projects");

    const projectHeader = await screen.findByRole("columnheader", { name: "Projeto" });
    expect(projectHeader).toBeInTheDocument();

    const web = screen.getByRole("link", { name: "acme/web" }).closest("tr");
    expect(web).toHaveTextContent("4"); // total de agentes
    const api = screen.getByRole("link", { name: "acme/api" }).closest("tr");
    expect(api).toHaveTextContent("4");

    // A linha inteira tem as três colunas numéricas: acme/web = 4 agentes, 1 needs_you, 1 working
    const webCells = web!.querySelectorAll("td");
    expect(webCells[1].textContent).toBe("4");
    expect(webCells[2].textContent).toBe("1");
    expect(webCells[3].textContent).toBe("1");

    // O link leva a /agents?project=<nome>
    await user.click(screen.getByRole("link", { name: "acme/web" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/agents"));
    expect(router.state.location.search).toEqual({ project: "acme/web" });
  });

  it("lists the hidden-project agents per machine and the note", async () => {
    renderHubApp("/projects");
    await screen.findByRole("columnheader", { name: "Projeto" });

    // hidden: laptop-b (working 1 + done 1 = 2), pc (idle 2 + needs_you 1 = 3)
    expect(await screen.findByText("pc: 3 agentes")).toBeInTheDocument();
    expect(screen.getByText("laptop-b: 2 agentes")).toBeInTheDocument();
    expect(screen.getByText("Projetos com 'não entregar' não aparecem aqui: eles nunca saem da máquina.")).toBeInTheDocument();
  });

  it("renders the empty state", async () => {
    renderHubApp(
      "/projects",
      defaultHubState({ agents: { agents: [], generated_ms: Date.now(), hidden: [] } }),
    );
    expect(await screen.findByText("Nenhum projeto exibido no hub.")).toBeInTheDocument();
  });

  it("says there are no hidden agents when the table has rows", async () => {
    renderHubApp(
      "/projects",
      defaultHubState({
        agents: { agents: AGENTS_RESPONSE.agents, generated_ms: Date.now(), hidden: [] },
      }),
    );
    await screen.findByText("acme/web");
    expect(screen.getByText("Nenhum agente em projetos ocultos.")).toBeInTheDocument();
  });

  it("renders the error state with retry", async () => {
    renderHubApp("/projects", defaultHubState({ agents: null }));
    expect(await screen.findByText("Algo deu errado. Tente novamente.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Tentar de novo" })).toBeInTheDocument();
  });
});
