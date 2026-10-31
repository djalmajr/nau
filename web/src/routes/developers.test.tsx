import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
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
});
