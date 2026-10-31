import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider, LOCALE_STORAGE_KEY, type TranslationKey, useI18n } from "@/lib/i18n";
import { renderWithProviders } from "@/test/render";

function Probe({ labelKey }: { labelKey: TranslationKey }) {
  const { locale, setLocale, t } = useI18n();
  return (
    <div>
      <span data-testid="label">{t(labelKey)}</span>
      <span data-testid="locale">{locale}</span>
      <button type="button" onClick={() => setLocale("en-US")}>
        switch
      </button>
    </div>
  );
}

beforeEach(() => {
  localStorage.clear();
});

describe("i18n", () => {
  it("defaults to pt-BR when no locale is stored", () => {
    localStorage.clear();
    renderWithProviders(<Probe labelKey="nav.overview" />);
    expect(screen.getByTestId("label")).toHaveTextContent("Visão geral");
    expect(screen.getByTestId("locale")).toHaveTextContent("pt-BR");
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
  });

  it("reads a stored locale", () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "en-US");
    renderWithProviders(<Probe labelKey="nav.overview" />);
    expect(screen.getByTestId("label")).toHaveTextContent("Overview");
    expect(screen.getByTestId("locale")).toHaveTextContent("en-US");
  });

  it("switching the locale changes the texts and persists the choice", async () => {
    const user = userEvent.setup();
    renderWithProviders(<Probe labelKey="nav.overview" />);
    expect(screen.getByTestId("label")).toHaveTextContent("Visão geral");

    await user.click(screen.getByRole("button", { name: "switch" }));

    expect(screen.getByTestId("label")).toHaveTextContent("Overview");
    expect(screen.getByTestId("locale")).toHaveTextContent("en-US");
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("en-US");
  });

  it("keeps the provider stable across renders", () => {
    const { rerender } = render(
      <I18nProvider>
        <Probe labelKey="nav.machines" />
      </I18nProvider>,
    );
    expect(screen.getByTestId("label")).toHaveTextContent("Máquinas");
    rerender(
      <I18nProvider>
        <Probe labelKey="nav.machines" />
      </I18nProvider>,
    );
    expect(screen.getByTestId("label")).toHaveTextContent("Máquinas");
  });

  it("labels the owner reason in pt-BR", () => {
    localStorage.clear();
    renderWithProviders(<Probe labelKey="reasons.owner" />);
    expect(screen.getByTestId("label")).toHaveTextContent("Esperando o dono");
  });

  it("labels the owner reason in en-US", () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "en-US");
    renderWithProviders(<Probe labelKey="reasons.owner" />);
    expect(screen.getByTestId("label")).toHaveTextContent("Waiting for the owner");
  });
});
