import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defaultHubState, renderHubApp, TEST_MEMBER } from "@/test/hub-harness";

beforeEach(() => {
  localStorage.clear();
});

/** O clipboard do jsdom não existe; stub com espia para provar o que seria
 *  copiado. O stub vai DEPOIS do userEvent.setup(): o user-event instala o
 *  próprio polyfill de clipboard quando o ambiente não tem, e ele sobrescreve
 *  um stub definido antes do setup. */
function stubClipboard() {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText },
  });
  return writeText;
}

/** Card da seção pelo título (o `Card` do design system carrega data-slot). */
function sectionCard(title: string): HTMLElement {
  const heading = screen.getByRole("heading", { name: title });
  const card = heading.closest('[data-slot="card"]');
  if (!card) throw new Error(`card da seção "${title}" não encontrado`);
  return card as HTMLElement;
}

describe("access: sharing", () => {
  it("lists who each own machine is shared with, with a remove button", async () => {
    renderHubApp("/access");
    await screen.findByRole("heading", { name: "Compartilhamento" });

    const laptopA = screen.getByTestId("share-m1");
    expect(laptopA).toHaveTextContent("Bruno Exemplo");
    expect(
      within(laptopA).getByRole("button", { name: "Remover — laptop-a" }),
    ).toBeInTheDocument();

    // O texto explicativo: só leitura, só projetos exibidos e contagens
    const card = sectionCard("Compartilhamento");
    expect(card).toHaveTextContent(
      "O compartilhamento é só leitura e mostra só os projetos exibidos e as contagens dos ocultos.",
    );
  });

  it("hides the sharing list and the form on a revoked machine", async () => {
    renderHubApp("/access");
    await screen.findByRole("heading", { name: "Compartilhamento" });

    // A old-laptop (m5) é da Ana e está revogada: só nome + selo "revogada",
    // sem a lista de compartilhamentos e sem o formulário de compartilhar.
    const oldLaptop = await screen.findByTestId("share-m5");
    expect(oldLaptop).toHaveTextContent("old-laptop");
    expect(oldLaptop).toHaveTextContent("revogada");
    expect(within(oldLaptop).queryByRole("form")).not.toBeInTheDocument();
    expect(within(oldLaptop).queryByRole("button", { name: "Compartilhar" })).not.toBeInTheDocument();
    expect(within(oldLaptop).queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("shares a machine by e-mail and the new share shows up", async () => {
    const user = userEvent.setup();
    renderHubApp("/access");
    await screen.findByRole("heading", { name: "Compartilhamento" });

    // Compartilha laptop-b (que não tinha compartilhado) com o e-mail conhecido
    const form = screen.getByTestId("share-m2");
    await user.type(within(form).getByLabelText("Compartilhar — laptop-b"), "bruno@example.com");
    await user.click(within(form).getByRole("button", { name: "Compartilhar" }));

    // A invalidação refaz a query e a máquina agora mostra o compartilhado
    await waitFor(() =>
      expect(within(screen.getByTestId("share-m2")).getByText("Bruno Exemplo")).toBeInTheDocument(),
    );
  });

  it("maps a 404 e-mail to the no-account message", async () => {
    const user = userEvent.setup();
    renderHubApp("/access");
    await screen.findByRole("heading", { name: "Compartilhamento" });

    const form = screen.getByTestId("share-m2");
    await user.type(within(form).getByLabelText("Compartilhar — laptop-b"), "desconhecido@example.com");
    await user.click(within(form).getByRole("button", { name: "Compartilhar" }));
    expect(await within(form).findByRole("alert")).toHaveTextContent("Nenhuma conta com esse e-mail.");
  });

  it("removes a share with the remove button", async () => {
    const user = userEvent.setup();
    const state = defaultHubState();
    renderHubApp("/access", state);
    await screen.findByRole("heading", { name: "Compartilhamento" });

    await user.click(
      within(screen.getByTestId("share-m1")).getByRole("button", { name: "Remover — laptop-a" }),
    );
    await waitFor(() =>
      expect(state.machines?.machines.find((m) => m.id === "m1")?.shared_with).toHaveLength(0),
    );
  });

  it("shows the empty sharing state when the user has no machines", async () => {
    const base = defaultHubState();
    renderHubApp(
      "/access",
      defaultHubState({
        machines: { machines: (base.machines?.machines ?? []).filter((m) => m.id === "m4") },
      }),
    );
    expect(await screen.findByText("Você não tem máquinas para compartilhar.")).toBeInTheDocument();
  });
});

describe("access: machine keys", () => {
  it("lists all machines with the revoke action only on the own ones", async () => {
    renderHubApp("/access");
    const section = await screen.findByRole("heading", { name: "Chaves de máquina" });
    const card = section.closest('[data-slot="card"]') as HTMLElement;

    expect(screen.getByTestId("machine-key-m1")).toBeInTheDocument();
    expect(screen.getByTestId("machine-key-m2")).toBeInTheDocument();
    expect(screen.getByTestId("machine-key-m3")).toBeInTheDocument();
    expect(screen.getByTestId("machine-key-m5")).toBeInTheDocument();
    const desk = screen.getByTestId("machine-key-m4");
    expect(desk).toHaveTextContent("desk");
    expect(within(desk).queryByRole("button", { name: "Revogar" })).not.toBeInTheDocument();
    expect(within(card).getAllByRole("button", { name: "Revogar" })).toHaveLength(3);
  });

  it("marks revoked machines and keeps them dimmed", async () => {
    renderHubApp("/access");
    await screen.findByRole("heading", { name: "Chaves de máquina" });
    const oldLaptop = screen.getByTestId("machine-key-m5");
    expect(oldLaptop).toHaveTextContent("revogada");
    expect(oldLaptop).toHaveClass("opacity-60");
    expect(within(oldLaptop).queryByRole("button", { name: "Revogar" })).not.toBeInTheDocument();
  });
});

describe("access: commands (coming soon)", () => {
  it("shows a dimmed section with 'Em breve' and no action", async () => {
    renderHubApp("/access");
    const section = await screen.findByRole("heading", { name: "Comandos nas suas máquinas" });
    const card = section.closest('[data-slot="card"]') as HTMLElement;
    expect(card).toHaveTextContent("Em breve");
    expect(card).toHaveClass("opacity-60");
    expect(within(card).queryAllByRole("button")).toHaveLength(0);
  });
});

describe("access: invites", () => {
  it("creates an invite and shows the link once, with copy and the warning", async () => {
    const user = userEvent.setup();
    const writeText = stubClipboard();
    renderHubApp("/access");
    await screen.findByRole("heading", { name: "Convites" });

    const emailInput = screen.getByLabelText("Criar convite");
    await user.type(emailInput, "carla2@example.com");
    await user.click(screen.getByRole("button", { name: "Criar convite" }));

    // O link aparece com o token no fragmento (origin/invite#<token>)
    const linkCode = await screen.findByText(new RegExp(`/invite#tok-\\d+$`));
    expect(linkCode.tagName).toBe("CODE");
    expect(
      screen.getByText("O link aparece uma única vez: depois que a tela fechar, ele não aparece de novo."),
    ).toBeInTheDocument();

    // Copiar copia exatamente o link mostrado
    await user.click(screen.getByRole("button", { name: "Copiar" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(linkCode.textContent));

    // O convite novo entra na lista de pendentes
    expect(screen.getByText("carla2@example.com")).toBeInTheDocument();
  });

  it("the invite link is not back after leaving the screen", async () => {
    const user = userEvent.setup();
    const { router } = renderHubApp("/access");
    await screen.findByRole("heading", { name: "Convites" });

    const emailInput = screen.getByLabelText("Criar convite");
    await user.type(emailInput, "carla2@example.com");
    await user.click(screen.getByRole("button", { name: "Criar convite" }));
    const linkCode = await screen.findByText(new RegExp(`/invite#tok-\\d+$`));
    expect(linkCode).toBeInTheDocument();

    // Sai da tela e volta: o token é mostrado uma única vez e some
    await user.click(screen.getByRole("link", { name: "Visão geral" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    await user.click(screen.getByRole("link", { name: "Acesso" }));
    await screen.findByRole("heading", { name: "Convites" });
    expect(screen.queryByText(new RegExp(`/invite#tok-\\d+$`))).not.toBeInTheDocument();
    // O convite segue pendente (o que some é o token, não o convite)
    expect(screen.getByText("carla2@example.com")).toBeInTheDocument();
  });

  it("revokes a pending invite", async () => {
    const user = userEvent.setup();
    const state = defaultHubState();
    renderHubApp("/access", state);
    await screen.findByRole("heading", { name: "Convites" });

    expect(screen.getByText("carla@example.com")).toBeInTheDocument();
    await user.click(
      within(sectionCard("Convites")).getByRole("button", { name: "Revogar" }),
    );
    await waitFor(() => expect(screen.queryByText("carla@example.com")).not.toBeInTheDocument());
    expect(state.invites.invites).toHaveLength(0);
  });

  it("hides the invites section for a member", async () => {
    renderHubApp("/access", defaultHubState({ sessionUser: TEST_MEMBER }));
    await screen.findByRole("heading", { name: "Compartilhamento" });
    expect(screen.queryByRole("heading", { name: "Convites" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Criar convite" })).not.toBeInTheDocument();
  });
});
