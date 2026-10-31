import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DEVELOPERS_RESPONSE } from "@/test/fixtures";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

describe("developers", () => {
  it("lists the developers with counts and marks the current user as 'você'", async () => {
    renderHubApp("/developers");

    const header = await screen.findByRole("columnheader", { name: "Máquinas" });
    expect(header).toBeInTheDocument();

    const rowAna = screen.getByText("Ana Exemplo").closest("tr");
    expect(rowAna).toHaveTextContent("ana@example.com");
    expect(rowAna).toHaveTextContent("você");
    expect(rowAna).toHaveTextContent("3"); // máquinas
    expect(rowAna).toHaveTextContent("1"); // compartilhamentos (cada contagem)

    const rowBruno = screen.getByText("Bruno Exemplo").closest("tr");
    expect(rowBruno).toHaveTextContent("bruno@example.com");
    expect(rowBruno).not.toHaveTextContent("você");
  });

  it("renders the empty state", async () => {
    renderHubApp("/developers", defaultHubState({ developers: { developers: [] } }));
    expect(await screen.findByText("Nenhum desenvolvedor por aqui.")).toBeInTheDocument();
  });

  it("renders the error state with retry", async () => {
    renderHubApp("/developers", defaultHubState({ developers: null }));
    expect(await screen.findByText("Algo deu errado. Tente novamente.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Tentar de novo" })).toBeInTheDocument();
  });

  it("keeps the fixture counts from the contract", async () => {
    renderHubApp("/developers");
    await screen.findByRole("columnheader", { name: "Máquinas" });
    // u2: 1 máquina, 0 que compartilha com o usuário, 1 que o usuário compartilha com
    const rowBruno = screen.getByText("Bruno Exemplo").closest("tr")!;
    const cells = rowBruno.querySelectorAll("td");
    expect(cells[2].textContent).toBe("1");
    expect(cells[3].textContent).toBe("0");
    expect(cells[4].textContent).toBe("1");
    expect(DEVELOPERS_RESPONSE.developers).toHaveLength(2);
  });

  it("refreshes the developer counts after sharing on a fresh cached page", async () => {
    const user = userEvent.setup();
    const state = defaultHubState();
    const initial = state.developers!.developers.find((d) => d.id === "u1")!.shared_by_me;
    const app = renderHubApp("/developers", state);
    await screen.findByRole("columnheader", { name: "Você compartilha com" });

    // Página fresca em cache: sem a invalidação, o staleTime manteria a
    // contagem velha na volta (o servidor já respondeu o novo número).
    app.queryClient.setDefaultOptions({
      queries: { retry: false, staleTime: 10_000 },
      mutations: { retry: false },
    });
    const original = fetch;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      const response = await original(input, init);
      if (init?.method === "POST" && String(input).endsWith("/machines/m2/shares") && response.ok) {
        state.developers!.developers.find((d) => d.id === "u1")!.shared_by_me = initial + 1;
      }
      return response;
    });

    await user.click(screen.getByRole("link", { name: "Acesso" }));
    const form = await screen.findByTestId("share-m2");
    await user.type(within(form).getByLabelText("Compartilhar — laptop-b"), "bruno@example.com");
    await user.click(within(form).getByRole("button", { name: "Compartilhar" }));
    await waitFor(() =>
      expect(within(screen.getByTestId("share-m2")).getByText("Bruno Exemplo")).toBeInTheDocument(),
    );

    await user.click(screen.getByRole("link", { name: "Desenvolvedores" }));
    await screen.findByRole("columnheader", { name: "Você compartilha com" });
    const row = screen.getByText("Ana Exemplo").closest("tr") as HTMLElement;
    const rendered = within(row).getAllByRole("cell")[4].textContent;
    expect(rendered).toBe(String(initial + 1));
  });
});
