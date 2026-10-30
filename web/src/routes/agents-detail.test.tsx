import { beforeEach, describe, expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderHubApp } from "@/test/hub-harness";

const BUILD_ID = "m2.w1:p2";

beforeEach(() => {
  localStorage.clear();
});

/** O <main> da página (o inset da sidebar também é um <main> e vem antes). */
function pageMain() {
  const mains = document.querySelectorAll("main");
  return mains[mains.length - 1];
}

describe("agent detail", () => {
  it("shows the summary, the history and the privacy note", async () => {
    renderHubApp(`/agents/${BUILD_ID}`);
    const title = await screen.findByRole("heading", {
      name: "Retry declined cards in the checkout worker",
    });
    expect(title).toBeInTheDocument();
    // Nome e papel abaixo do título
    expect(screen.getByText("build · implementer")).toBeInTheDocument();

    // Resumo: estado com motivo, assistente/modelo, esforço, projeto,
    // sessão, terminal, máquina (online), desenvolvedor e relatório
    // ("Precisa de você"/"Aprovação" aparecem no resumo E no histórico).
    expect(screen.getAllByText("Precisa de você")).toHaveLength(2);
    expect(screen.getAllByText("Aprovação")).toHaveLength(2);
    expect(screen.getByText("cli-b · example/model-1")).toBeInTheDocument();
    expect(screen.getByText("high")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
    expect(screen.getByText("w1")).toBeInTheDocument();
    expect(screen.getByText("w1:p2")).toBeInTheDocument();
    expect(screen.getByText("laptop-b")).toBeInTheDocument();
    expect(screen.getByText("online")).toBeInTheDocument();
    expect(screen.getByText("Ana Exemplo")).toBeInTheDocument();
    expect(screen.getByText("Relatório pronto")).toBeInTheDocument();
    expect(screen.getByText("Sim")).toBeInTheDocument();

    // Histórico: duas entradas — a segunda tem motivo vazio ("—")
    expect(screen.getByText("Histórico")).toBeInTheDocument();
    expect(screen.getByText("Trabalhando")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();

    // Nota discreta
    expect(
      screen.getByText(
        "O que nunca sai da máquina: brief, relatório, tela, caminhos, ambiente e saída de comandos.",
      ),
    ).toBeInTheDocument();
  });

  it("shows the empty history message when there are no changes", async () => {
    renderHubApp("/agents/m1.w1:p4");
    expect(await screen.findByText("Sem mudanças nos últimos 7 dias")).toBeInTheDocument();
  });

  it("the back bar returns to /agents preserving view and filters", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp(`/agents/${BUILD_ID}?view=list&project=acme%2Fweb`);
    await screen.findByRole("heading", { name: "Retry declined cards in the checkout worker" });
    const back = within(pageMain()).getByRole("link", { name: "Agentes" });
    await user.click(back);
    expect(router.state.location.pathname).toBe("/agents");
    expect(router.state.location.search).toMatchObject({ view: "list", project: "acme/web" });
  });

  it("shows the not-found page with the back link for an unknown agent", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp("/agents/nao-existe");
    expect(await screen.findByText("Agente não encontrado")).toBeInTheDocument();
    expect(
      screen.getByText("O agente não existe ou a sessão da máquina terminou."),
    ).toBeInTheDocument();
    await user.click(within(pageMain()).getByRole("link", { name: "Agentes" }));
    expect(router.state.location.pathname).toBe("/agents");
  });

  it("keeps the filters when going back from the 404 screen", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    const { router } = renderHubApp("/agents/nao-existe?view=list&q=xyz");
    await screen.findByText("Agente não encontrado");
    await user.click(within(pageMain()).getByRole("link", { name: "Agentes" }));
    expect(router.state.location.pathname).toBe("/agents");
    expect(router.state.location.search).toMatchObject({ view: "list", q: "xyz" });
  });
});
