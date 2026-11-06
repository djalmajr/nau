import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
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

  it("confirms the code (hyphen and case optional), closes the dialog, shows one success toast outside it, refetches the list and returns focus to the trigger", async () => {
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

    // Success closes the dialog on its own: the success step and its close
    // button are gone from it, and the confirmation is born OUTSIDE the
    // dialog, in the notifications region; it survives the dialog exit.
    const toast = await screen.findByText("Máquina conectada.");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Conectar máquina" })).not.toBeInTheDocument());
    const region = screen.getByRole("region", { name: "Notificações" });
    expect(region).toContainElement(toast);
    // Notifies once.
    expect(screen.getAllByText("Máquina conectada.")).toHaveLength(1);
    expect(state.calls.some((call) => call.method === "POST" && call.url === "/api/pairings/confirm")).toBe(true);

    // Success invalidates the machines: the list behind refetches on its
    // own (the mutation's invalidation).
    await waitFor(() => expect(machineCalls()).toBeGreaterThan(before));
    // Focus returns to the button that opened the dialog.
    expect(trigger).toHaveFocus();
  });

  it("dismisses the success toast by keyboard with its labeled button, and focus does not stay inside the toast region", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines", defaultHubState());

    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    await user.type(await screen.findByLabelText("Código XXXX-XXXX"), "abcd-efgh");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    await screen.findByText("Máquina conectada.");

    // Keyboard path: focusing the notifications region expands the toast
    // and puts its labeled dismiss in the accessibility tree (Base UI
    // contract for the collapsed state). The toast did not steal focus
    // when it appeared.
    const region = screen.getByRole("region", { name: "Notificações" });
    region.focus();
    const dismiss = await screen.findByRole("button", { name: "Descartar" });
    // Next keyboard step: focus moves from the region to the dismiss and
    // Enter activates it.
    dismiss.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.queryByText("Máquina conectada.")).not.toBeInTheDocument());

    expect(region.contains(document.activeElement)).toBe(false);
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

  it("keeps a generic (500) error in the open dialog, without a toast", async () => {
    const user = userEvent.setup();
    renderHubApp("/machines", defaultHubState({ pairingFail: 500 }));

    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    const input = await screen.findByLabelText("Código XXXX-XXXX");
    await user.type(input, "abcd-efgh");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Algo deu errado. Tente novamente.");
    // The generic error keeps the dialog open and shows no toast.
    expect(screen.getByRole("dialog", { name: "Conectar máquina" })).toBeInTheDocument();
    expect(screen.queryByText("Máquina conectada.")).not.toBeInTheDocument();
  });

  it("submits once: the submit button disables while the request is in flight and a second click starts no second request", async () => {
    const user = userEvent.setup();
    const { release, state } = await submitAndHold(user);

    const submit = screen.getByRole("button", { name: "Conectar" });
    expect(submit).toBeDisabled();
    await user.click(submit); // disabled: no second POST

    release();
    await screen.findByText("Máquina conectada.");
    expect(state.calls.filter((call) => call.method === "POST" && call.url === "/api/pairings/confirm")).toHaveLength(1);
  });

  it("resets the state (code, error and copied) when closed and reopened", async () => {
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

  // Fecha pelo X e reabre, devolvendo o modal novo.
  async function closeAndReopen(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Conectar máquina" }));
    return screen.findByRole("dialog");
  }

  // Abre o modal com o POST segurado, envia e devolve como liberá-lo.
  async function submitAndHold(user: ReturnType<typeof userEvent.setup>, pairingFail?: number) {
    let release = () => {};
    const pairingGate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const state = defaultHubState({ pairingFail, pairingGate });
    renderHubApp("/machines", state);
    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    await user.type(await screen.findByLabelText("Código XXXX-XXXX"), "abcd-efgh");
    await user.click(screen.getByRole("button", { name: "Conectar" }));
    await waitFor(() =>
      expect(state.calls.some((call) => call.method === "POST" && call.url === "/api/pairings/confirm")).toBe(true),
    );
    return { release, state };
  }

  it("does not let a submit that succeeds after closing reach the reopened dialog, or show a stale success toast, and still refetches the list", async () => {
    const user = userEvent.setup();
    const { release, state } = await submitAndHold(user);
    const dialog = await closeAndReopen(user);
    const machineCalls = () => state.calls.filter((call) => call.url === "/api/machines").length;
    const before = machineCalls();

    release();
    // A máquina foi confirmada: a lista por trás refaz a consulta.
    await waitFor(() => expect(machineCalls()).toBeGreaterThan(before));

    expect(within(dialog).queryByRole("status")).not.toBeInTheDocument();
    // A late success shows no toast for the opening that already closed.
    expect(screen.queryByText("Máquina conectada.")).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText("Código XXXX-XXXX")).toHaveValue("");
  });

  it("does not let a submit that fails after closing reach the reopened dialog", async () => {
    const user = userEvent.setup();
    const { release } = await submitAndHold(user, 400);
    const dialog = await closeAndReopen(user);

    await act(async () => {
      release();
      await new Promise((resolve) => setTimeout(resolve, 50));
    });

    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText("Código XXXX-XXXX")).toHaveValue("");
  });

  it("does not let a copy that finishes after closing mark the reopened dialog as copied", async () => {
    const user = userEvent.setup();
    let release = () => {};
    const writeText = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    renderHubApp("/machines");

    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Copiar" }));
    expect(writeText).toHaveBeenCalled();
    const dialog = await closeAndReopen(user);

    await act(async () => {
      release();
      await Promise.resolve();
    });

    expect(within(dialog).getByRole("button", { name: "Copiar" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Copiado" })).not.toBeInTheDocument();
  });

  it("gives each opening its own 2 s copied indicator", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) });
    stubClipboard();
    renderHubApp("/machines");

    await user.click(await screen.findByRole("button", { name: "Conectar máquina" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Copiar" }));
    await within(screen.getByRole("dialog")).findByRole("button", { name: "Copiado" });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1500);
    });
    const dialog = await closeAndReopen(user);
    await user.click(within(dialog).getByRole("button", { name: "Copiar" }));
    await within(dialog).findByRole("button", { name: "Copiado" });

    // O timer da abertura anterior venceria aqui; o indicador novo continua.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(within(dialog).getByRole("button", { name: "Copiado" })).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(within(dialog).getByRole("button", { name: "Copiar" })).toBeInTheDocument();
  });

  it("starts from zero when reopened during the exit animation, before the popup unmounts", async () => {
    const user = userEvent.setup();
    const { release } = await submitAndHold(user);
    const firstInput = screen.getByLabelText("Código XXXX-XXXX");

    // O setup do jsdom diz que não há animação, e o popup desmontaria ao
    // fechar. Aqui a saída fica pendente, como os 100 ms do navegador.
    const exit = new Promise<void>(() => {});
    vi.spyOn(Element.prototype, "getAnimations").mockReturnValue([{ finished: exit } as unknown as Animation]);
    await user.keyboard("{Escape}");
    const popup = screen.getByText("Conectar máquina", { selector: "h2" }).closest('[data-slot="dialog-content"]');
    await waitFor(() => expect(popup).toHaveAttribute("data-closed"));
    expect(firstInput).toBeInTheDocument();

    // Reabre com o popup ainda montado: é o que Tab e Enter fazem no navegador.
    const trigger = document.querySelector<HTMLElement>('[data-slot="dialog-trigger"]');
    if (!trigger) throw new Error("trigger not found");
    fireEvent.click(trigger);
    const input = await screen.findByLabelText("Código XXXX-XXXX");
    expect(input).not.toBe(firstInput);
    expect(input).toHaveValue("");

    await act(async () => {
      release();
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    // The success of the already-closed opening shows no toast: its body is
    // still mounted during the exit; what decides is whether the dialog is
    // open when the response arrives.
    expect(screen.queryByText("Máquina conectada.")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Código XXXX-XXXX")).toHaveValue("");
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
