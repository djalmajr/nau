import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { defaultHubState, renderHubApp } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

const NEW_PASSWORD = "nova-senha-123";

async function fillPasswordForm(
  user: ReturnType<typeof userEvent.setup>,
  current = "senha-atual-123",
  next = NEW_PASSWORD,
  confirm = NEW_PASSWORD,
) {
  await user.type(screen.getByLabelText("Senha atual"), current);
  await user.type(screen.getByLabelText("Nova senha"), next);
  await user.type(screen.getByLabelText("Confirmar nova senha"), confirm);
  await user.click(screen.getByRole("button", { name: "Trocar senha" }));
}

describe("settings: account", () => {
  it("shows the name and e-mail read-only", async () => {
    renderHubApp("/settings");
    const heading = await screen.findByRole("heading", { name: "Conta" });
    const card = heading.closest('[data-slot="card"]') as HTMLElement;
    expect(card).toHaveTextContent("Ana Exemplo");
    expect(card).toHaveTextContent("ana@example.com");
    expect(within(card).queryAllByRole("textbox")).toHaveLength(0);
  });
});

describe("settings: change password", () => {
  it("changes the password and confirms with a message", async () => {
    const user = userEvent.setup();
    const state = defaultHubState();
    renderHubApp("/settings", state);
    await screen.findByRole("heading", { name: "Trocar senha" });

    await fillPasswordForm(user);
    expect(await screen.findByRole("status")).toHaveTextContent("Senha trocada.");
    expect(state.calls.some((call) => call.method === "POST" && call.url === "/api/session/password")).toBe(true);
    // Os campos ficam limpos depois do sucesso
    expect((screen.getByLabelText("Senha atual") as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText("Nova senha") as HTMLInputElement).value).toBe("");
  });

  it("maps 401 to the wrong-current message", async () => {
    const user = userEvent.setup();
    renderHubApp("/settings", defaultHubState({ passwordFail: 401 }));
    await screen.findByRole("heading", { name: "Trocar senha" });

    await fillPasswordForm(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("Senha atual incorreta.");
  });

  it("maps 400 weak_password to the length message", async () => {
    const user = userEvent.setup();
    renderHubApp("/settings", defaultHubState({ passwordFail: 400 }));
    await screen.findByRole("heading", { name: "Trocar senha" });

    await fillPasswordForm(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("A nova senha precisa ter de 12 a 128 caracteres.");
  });

  it("rejects a mismatched confirmation without calling the API", async () => {
    const user = userEvent.setup();
    const state = defaultHubState();
    renderHubApp("/settings", state);
    await screen.findByRole("heading", { name: "Trocar senha" });

    await fillPasswordForm(user, "senha-atual-123", NEW_PASSWORD, "outra-senha-456");
    expect(await screen.findByRole("alert")).toHaveTextContent("As novas senhas não conferem.");
    expect(state.calls.some((call) => call.url === "/api/session/password")).toBe(false);
  });

  it("rejects a too-short new password locally", async () => {
    const user = userEvent.setup();
    const state = defaultHubState();
    renderHubApp("/settings", state);
    await screen.findByRole("heading", { name: "Trocar senha" });

    await fillPasswordForm(user, "senha-atual-123", "curta", "curta");
    expect(await screen.findByRole("alert")).toHaveTextContent("A nova senha precisa ter de 12 a 128 caracteres.");
    expect(state.calls.some((call) => call.url === "/api/session/password")).toBe(false);
  });
});

describe("settings: appearance", () => {
  it("reuses the header theme and language selectors", async () => {
    renderHubApp("/settings");
    const heading = await screen.findByRole("heading", { name: "Aparência" });
    const card = heading.closest('[data-slot="card"]') as HTMLElement;

    // Os mesmos controles do header, dentro do card de Aparência
    expect(within(card).getByRole("button", { name: /Tema/ })).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Idioma" })).toBeInTheDocument();
  });
});

describe("settings: hub", () => {
  it("shows the hub version read-only", async () => {
    renderHubApp("/settings");
    const heading = await screen.findByRole("heading", { name: "Hub" });
    const card = heading.closest('[data-slot="card"]') as HTMLElement;
    await waitFor(() => expect(within(card).getByText("0.1.0")).toBeInTheDocument());
    expect(within(card).queryAllByRole("textbox")).toHaveLength(0);
  });
});
