import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { defaultHubState, renderHubApp, TEST_USER } from "@/test/hub-harness";

const TOKEN = "tok-abc-123";

beforeEach(() => {
  localStorage.clear();
  window.history.replaceState(null, "", "/");
});

afterEach(() => {
  window.history.replaceState(null, "", "/");
});

/** Põe o token onde o contrato diz que ele vive: no fragmento da URL. */
function withInviteToken() {
  window.location.hash = `#${TOKEN}`;
}

describe("invite", () => {
  it("accepts the token from the fragment, cleans the URL and enters the shell", async () => {
    const user = userEvent.setup();
    withInviteToken();
    const { router, queryClient } = renderHubApp("/invite");

    const name = await screen.findByLabelText("Nome");

    // O fragmento é limpo quando a tela lê o token (o token não fica na URL)
    expect(window.location.hash).toBe("");

    await user.type(name, "Carla Exemplo");
    await user.type(await screen.findByLabelText("Senha"), "senha-convidada-1");
    await user.click(screen.getByRole("button", { name: "Criar conta" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(queryClient.getQueryData(["session"])).toEqual(TEST_USER);

    // O token não vaza para o armazenamento
    const stored = Object.values(localStorage).join("\n");
    expect(stored).not.toContain(TOKEN);
  });

  it("accepts the token even when it has no hyphen or is lowercase", async () => {
    // O token é opaco: a tela o envia como lido do fragmento
    window.location.hash = "#abc";
    const user = userEvent.setup();
    const state = defaultHubState();
    renderHubApp("/invite", state);

    await user.type(await screen.findByLabelText("Nome"), "Carla Exemplo");
    await user.type(await screen.findByLabelText("Senha"), "senha-convidada-1");
    await user.click(screen.getByRole("button", { name: "Criar conta" }));

    const call = state.calls.find((call) => call.url === "/api/invites/accept");
    expect(call).toBeDefined();
    expect(JSON.parse(call!.body!)).toEqual({
      name: "Carla Exemplo",
      password: "senha-convidada-1",
      token: "abc",
    });
  });

  it("shows the invalid message when there is no token in the fragment", async () => {
    renderHubApp("/invite");
    expect(await screen.findByRole("alert")).toHaveTextContent("Convite inválido ou expirado.");
    expect(screen.queryByRole("button", { name: "Criar conta" })).not.toBeInTheDocument();
  });

  it("maps 400 to the invalid-invite message", async () => {
    const user = userEvent.setup();
    withInviteToken();
    renderHubApp("/invite", defaultHubState({ inviteAcceptFail: 400 }));

    await user.type(await screen.findByLabelText("Nome"), "Carla Exemplo");
    await user.type(await screen.findByLabelText("Senha"), "senha-convidada-1");
    await user.click(screen.getByRole("button", { name: "Criar conta" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Convite inválido ou expirado.");
  });

  it("maps 400 weak_password to the password length message", async () => {
    const user = userEvent.setup();
    withInviteToken();
    renderHubApp("/invite");

    // A senha passa na validação local (100 caracteres, dentro de 12 a 128)
    // mas responde 400 weak_password no servidor: 200 bytes, fora de 12 a
    // 128. A tela mapeia o código para a mensagem de tamanho, não a genérica.
    await user.type(await screen.findByLabelText("Nome"), "Carla Exemplo");
    await user.type(await screen.findByLabelText("Senha"), "ç".repeat(100));
    await user.click(screen.getByRole("button", { name: "Criar conta" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "A nova senha precisa ter de 12 a 128 caracteres.",
    );
  });

  it("maps 409 to the email-taken message", async () => {
    const user = userEvent.setup();
    withInviteToken();
    renderHubApp("/invite", defaultHubState({ inviteAcceptFail: 409 }));

    await user.type(await screen.findByLabelText("Nome"), "Carla Exemplo");
    await user.type(await screen.findByLabelText("Senha"), "senha-convidada-1");
    await user.click(screen.getByRole("button", { name: "Criar conta" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Já existe uma conta com esse e-mail.");
  });

  it("keeps the submit disabled until name and a 12+ char password exist", async () => {
    withInviteToken();
    renderHubApp("/invite");

    const submit = await screen.findByRole("button", { name: "Criar conta" });
    expect(submit).toBeDisabled();

    const user = userEvent.setup();
    const name = await screen.findByLabelText("Nome");
    await user.type(name, "Carla Exemplo");
    expect(submit).toBeDisabled();
    const password = await screen.findByLabelText("Senha");
    await user.type(password, "doze");
    expect(submit).toBeDisabled();
    await user.type(password, "-caracteres-1234567");
    expect(submit).toBeEnabled();
  });
});
