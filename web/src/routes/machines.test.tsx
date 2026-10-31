import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

describe("machines", () => {
  it("lists own and shared machines with status, developer, last seen and shares", async () => {
    renderHubApp("/machines");

    // Seções: as próprias (4) e a compartilhada (1)
    expect(await screen.findByRole("heading", { name: "Suas máquinas" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Compartilhadas com você" })).toBeInTheDocument();

    const own = screen.getByRole("heading", { name: "Suas máquinas" }).parentElement;
    const laptopA = within(own!).getByTestId("machine-card-m1");
    expect(laptopA).toHaveTextContent("laptop-a");
    expect(laptopA).toHaveTextContent("online");
    expect(laptopA).toHaveTextContent("Ana Exemplo");
    expect(laptopA).toHaveTextContent("Compartilhada com");
    expect(laptopA).toHaveTextContent("Bruno Exemplo");

    // Compartilhada: etiqueta "Compartilhada com você", sem "Compartilhada com"
    const desk = screen.getByTestId("machine-card-m4");
    expect(desk).toHaveTextContent("desk");
    expect(desk).toHaveTextContent("Compartilhada com você");
    expect(desk).not.toHaveTextContent("Compartilhada com Bruno");

    // Offline e nunca online: selo offline
    const pc = screen.getByTestId("machine-card-m3");
    expect(pc).toHaveTextContent("offline");

    // Revogada: esmaecida + selo "revogada" + sem botão de revogar
    const oldLaptop = screen.getByTestId("machine-card-m5");
    expect(oldLaptop).toHaveTextContent("revogada");
    expect(oldLaptop).toHaveClass("opacity-60");
    expect(within(oldLaptop).queryByRole("button", { name: "Revogar" })).not.toBeInTheDocument();

    // Botão de revogar só nas próprias (desk é compartilhada)
    expect(within(desk).queryByRole("button", { name: "Revogar" })).not.toBeInTheDocument();
  });

  it("renders the empty state when there are no machines", async () => {
    renderHubApp("/machines", defaultHubState({ machines: { machines: [] } }));
    expect(await screen.findByText("Nenhuma máquina conectada.")).toBeInTheDocument();
  });

  it("renders the error state with retry when the request fails", async () => {
    renderHubApp("/machines", defaultHubState({ machines: null }));
    expect(await screen.findByText("Algo deu errado. Tente novamente.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Tentar de novo" })).toBeInTheDocument();
  });

  it("revokes only after the confirmation dialog, never via confirm()", async () => {
    const user = userEvent.setup();
    const state = defaultHubState();
    const { queryClient } = renderHubApp("/machines", state);

    const laptopB = await screen.findByTestId("machine-card-m2");
    const revokeButton = within(laptopB).getByRole("button", { name: "Revogar" });

    // Abre o diálogo do design system (o teste vê o `role=dialog` na
    // árvore; `window.confirm` nunca é chamado — o fetch falso quebra se o
    // código tentar abrir a caixa nativa)
    await user.click(revokeButton);
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Revogar a chave de laptop-b?");
    expect(dialog).toHaveTextContent("A máquina para de entregar na hora e esta ação não pode ser desfeita.");

    // Cancelar não chama a API
    await user.click(within(dialog).getByRole("button", { name: "Cancelar" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(state.calls.some((call) => call.method === "DELETE" && call.url.startsWith("/api/machines/"))).toBe(false);

    // Confirmar chama DELETE e a máquina vira revogada (invalidação da query)
    await user.click(within(laptopB).getByRole("button", { name: "Revogar" }));
    const dialog2 = await screen.findByRole("dialog");
    await user.click(within(dialog2).getByRole("button", { name: "Revogar" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(state.calls.some((call) => call.method === "DELETE" && call.url === "/api/machines/m2")).toBe(true);

    const refreshed = await screen.findByTestId("machine-card-m2");
    await waitFor(() => expect(refreshed).toHaveTextContent("revogada"));
    expect(queryClient.getQueryData(["machines", "list", "u1"])).toBeDefined();
  });
});
