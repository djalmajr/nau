import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { applyTheme, normalizeTheme, resolveTheme, THEME_STORAGE_KEY, useTheme } from "./theme";
import { renderWithProviders } from "@/test/render";

describe("theme", () => {
  it("normalizes unknown values to system", () => {
    expect(normalizeTheme("dark")).toBe("dark");
    expect(normalizeTheme("light")).toBe("light");
    expect(normalizeTheme("system")).toBe("system");
    expect(normalizeTheme(null)).toBe("system");
    expect(normalizeTheme("neon")).toBe("system");
  });

  it("applies the dark class and color scheme", () => {
    const root = document.createElement("html");

    applyTheme("dark", root);
    expect(root.classList.contains("dark")).toBe(true);
    expect(root.style.colorScheme).toBe("dark");

    applyTheme("light", root);
    expect(root.classList.contains("dark")).toBe(false);
    expect(root.style.colorScheme).toBe("light");
  });

  it("resolves system to light when no dark preference is set", () => {
    expect(resolveTheme("system")).toBe("light");
  });

  it("switching the theme applies the class on <html> and persists it", async () => {
    localStorage.clear();
    const user = userEvent.setup();

    function Controls() {
      const { setTheme, theme } = useTheme();
      return (
        <div>
          <span data-testid="theme">{theme}</span>
          <button type="button" onClick={() => setTheme("dark")}>
            to-dark
          </button>
          <button type="button" onClick={() => setTheme("light")}>
            to-light
          </button>
        </div>
      );
    }

    renderWithProviders(<Controls />);

    // O provider aplica o tema inicial (system → light neste jsdom).
    expect(document.documentElement.classList.contains("dark")).toBe(false);

    await user.click(screen.getByRole("button", { name: "to-dark" }));
    expect(screen.getByTestId("theme")).toHaveTextContent("dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(document.documentElement.style.colorScheme).toBe("dark");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");

    await user.click(screen.getByRole("button", { name: "to-light" }));
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
  });

  it("restores a stored theme on mount", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, "dark");

    function Probe() {
      const { theme } = useTheme();
      return <span data-testid="theme">{theme}</span>;
    }

    renderWithProviders(<Probe />);

    expect(await screen.findByTestId("theme")).toHaveTextContent("dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });
});
