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

describe("machines connect", () => {
  it("shows the own header title and keeps the back bar to machines", async () => {
    renderHubApp("/machines/connect");
    await screen.findByRole("heading", { name: "1. Rode o comando na máquina" });

    // O header mostra o próprio título (e não o "Máquinas" da lista).
    expect(screen.getByText("Conectar máquina")).toBeInTheDocument();

    // A barra de volta continua: o link fica no main do conteúdo (o nav da
    // casca tem também o item "Máquinas").
    const mains = Array.from(document.querySelectorAll("main"));
    const content = within(mains[mains.length - 1] as HTMLElement);
    expect(content.getByRole("link", { name: "Máquinas" })).toHaveAttribute("href", "/machines");
  });

  it("shows the command built from the origin, with a working copy button", async () => {
    const user = userEvent.setup();
    const writeText = stubClipboard();
    renderHubApp("/machines/connect");

    await screen.findByRole("heading", { name: "1. Rode o comando na máquina" });
    const command = screen.getByText(`nau connect ${window.location.origin}`);
    expect(command.tagName).toBe("CODE");

    await user.click(screen.getByRole("button", { name: "Copiar" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`nau connect ${window.location.origin}`));
    expect(await screen.findByRole("button", { name: "Copiado" })).toBeInTheDocument();
  });

  it("accepts the code with or without hyphen and shows success with a link to /machines", async () => {
    const user = userEvent.setup();
    const state = defaultHubState(); // código válido "abcd-efgh"
    const { router } = renderHubApp("/machines/connect", state);

    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "ABCDEFGH"); // sem hífen, caixa alta
    await user.click(screen.getByRole("button", { name: "Conectar" }));

    await screen.findByRole("status");
    expect(screen.getByRole("status")).toHaveTextContent("Máquina conectada.");
    expect(state.calls.some((call) => call.method === "POST" && call.url === "/api/pairings/confirm")).toBe(true);
    const link = screen.getByRole("link", { name: "Ver máquinas" });
    await user.click(link);
    await waitFor(() => expect(router.state.location.pathname).toBe("/machines"));
  });

  it("maps 400 to the invalid-code message", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines/connect", defaultHubState({ pairingFail: 400 }));

    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "zzzz-zzzz");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Código inválido ou expirado.");
  });

  it("maps 429 to the rate-limit message", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines/connect", defaultHubState({ pairingFail: 429 }));

    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "zzzz-zzzz");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Muitas tentativas. Tente de novo em alguns minutos.");
  });
});
