import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

// O clipboard do jsdom não existe; stub com espia para provar o que seria
// copiado. O stub vai DEPOIS do userEvent.setup(): o user-event instala o
// próprio polyfill de clipboard quando o ambiente não tem, e ele sobrescreve
// um stub definido antes do setup.
function stubClipboard() {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText },
  });
  return writeText;
}

describe("connect machine dialog", () => {
  it("opens from the page button with the title and the command built from the origin, with a working copy button", async () => {
    const user = userEvent.setup();
    const writeText = stubClipboard();
    renderHubApp("/machines");

    // O botão vive no conteúdo da página (não há mais ação no cabeçalho).
    const trigger = await screen.findByRole("button", { name: "Conectar máquina" });
    await user.click(trigger);

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Conectar máquina" })).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "1. Rode o comando na máquina" })).toBeInTheDocument();
    const command = within(dialog).getByText(`nau connect ${window.location.origin}`);
    expect(command.tagName).toBe("CODE");

    await user.click(within(dialog).getByRole("button", { name: "Copiar" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`nau connect ${window.location.origin}`));
    expect(within(dialog).getByRole("button", { name: "Copiado" })).toBeInTheDocument();
  });

  it("accepts the code with or without hyphen, shows success and the list behind refetches", async () => {
    const user = userEvent.setup();
    const state = defaultHubState(); // código válido "abcd-efgh"
    renderHubApp("/machines", state);

    // Contagem de base: o botão só aparece com a lista carregada, então a
    // consulta inicial de /api/machines já ocorreu.
    const machineCalls = () => state.calls.filter((call) => call.url === "/api/machines").length;
    const trigger = await screen.findByRole("button", { name: "Conectar máquina" });
    const before = machineCalls();
    expect(before).toBeGreaterThan(0);

    await user.click(trigger);
    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "ABCDEFGH"); // sem hífen, caixa alta
    await user.click(screen.getByRole("button", { name: "Conectar" }));

    await screen.findByRole("status");
    expect(screen.getByRole("status")).toHaveTextContent("Máquina conectada.");
    expect(state.calls.some((call) => call.method === "POST" && call.url === "/api/pairings/confirm")).toBe(true);

    // O sucesso invalida as máquinas: a lista por trás refaz a consulta sem
    // o usuário fazer nada (a invalidação da mutação); o botão de sucesso
    // fecha o modal.
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Ver máquinas" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(machineCalls()).toBeGreaterThan(before));
  });

  it("maps 400 to the invalid-code message", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines", defaultHubState({ pairingFail: 400 }));

    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "zzzz-zzzz");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Código inválido ou expirado.");
  });

  it("maps 429 to the rate-limit message", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines", defaultHubState({ pairingFail: 429 }));

    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "zzzz-zzzz");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Muitas tentativas. Tente de novo em alguns minutos.");
  });

  it("resets the state (code, error, success and copied) when closed and reopened", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines", defaultHubState({ pairingFail: 400 }));

    // Abre, digita um código e vê o erro.
    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "zzzz-zzzz");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Código inválido ou expirado.");

    // Fecha pelo X do diálogo e reabre: código e erro somem (estado no
    // início), e o formulário volta a aceitar o fluxo do zero.
    await user.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Conectar máquina" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText("Código XXXX-XXXX")).toHaveValue("");
  });

  it("shows the button with the empty list and opens the dialog from it", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines", defaultHubState({ machines: { machines: [] } }));

    // O botão aparece com a lista vazia (o fluxo de conexão é a saída do
    // estado vazio).
    expect(await screen.findByText("Nenhuma máquina conectada.")).toBeInTheDocument();
    const trigger = screen.getByRole("button", { name: "Conectar máquina" });
    expect(trigger).toBeInTheDocument();

    await user.click(trigger);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "1. Rode o comando na máquina" })).toBeInTheDocument();
  });
});
