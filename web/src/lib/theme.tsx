import * as React from "react";

export const THEME_STORAGE_KEY = "nauTheme";

const DEFAULT_THEME = "system";

export type ResolvedTheme = "dark" | "light";
export type ThemePreference = "dark" | "light" | "system";

type ThemeContextValue = {
  resolvedTheme: ResolvedTheme;
  setTheme: (theme: ThemePreference) => void;
  theme: ThemePreference;
};

const ThemeContext = React.createContext<ThemeContextValue | null>(null);

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setThemeState] = React.useState(readStoredTheme);
  const [resolvedTheme, setResolvedTheme] = React.useState<ResolvedTheme>(() => resolveTheme(theme));

  React.useEffect(() => {
    function syncTheme() {
      setResolvedTheme(applyTheme(theme));
    }

    syncTheme();
    localStorage.setItem(THEME_STORAGE_KEY, theme);

    if (theme !== "system" || typeof window === "undefined" || !window.matchMedia) return;
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    query.addEventListener("change", syncTheme);
    return () => query.removeEventListener("change", syncTheme);
  }, [theme]);

  const setTheme = React.useCallback((next: ThemePreference) => {
    setThemeState(next);
  }, []);

  const value = React.useMemo(() => ({ resolvedTheme, setTheme, theme }), [resolvedTheme, setTheme, theme]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function applyTheme(theme: ThemePreference, root: HTMLElement = document.documentElement): ResolvedTheme {
  const resolved = resolveTheme(theme);
  root.classList.toggle("dark", resolved === "dark");
  root.style.colorScheme = resolved;
  return resolved;
}

export function normalizeTheme(value: string | null | undefined): ThemePreference {
  if (value === "dark" || value === "light" || value === "system") return value;
  return DEFAULT_THEME;
}

export function resolveTheme(theme: ThemePreference): ResolvedTheme {
  if (theme !== "system") return theme;
  if (typeof window === "undefined" || !window.matchMedia) return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function useTheme() {
  const context = React.useContext(ThemeContext);
  if (!context) throw new Error("useTheme must be used within <ThemeProvider>");
  return context;
}

function readStoredTheme(): ThemePreference {
  if (typeof window === "undefined") return DEFAULT_THEME;
  return normalizeTheme(localStorage.getItem(THEME_STORAGE_KEY));
}
