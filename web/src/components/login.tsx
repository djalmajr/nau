import * as React from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Cpu, LogIn, Monitor, Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api, ApiError } from "@/lib/api";
import { LOCALE_OPTIONS, useI18n, type Locale } from "@/lib/i18n";
import { safeNext, SESSION_QUERY_KEY } from "@/lib/session";
import { type ThemePreference, useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

// A senha só existe neste componente, no estado local: não vai para log,
// URL nem armazenamento.
export function LoginScreen() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { next } = useSearch({ strict: false }) as { next?: string };

  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");

  const canSubmit = email.trim().length > 0 && password.length > 0 && !busy;

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError("");
    try {
      const res = await api.login(email.trim(), password);
      // A sessão vira o estado da query antes da navegação, então a rota
      // protegida não pisca o estado de carga.
      queryClient.setQueryData(SESSION_QUERY_KEY, res.user);
      navigate({ to: safeNext(next), replace: true });
    } catch (err) {
      // 401 = credenciais; 429 = rate limit — cada um tem mensagem própria.
      // O resto (rede, 5xx) também tem mensagem genérica; a senha nunca é
      // incluída em erro.
      if (err instanceof ApiError) {
        if (err.status === 401) setError(t("auth.invalidCredentials"));
        else if (err.status === 429) setError(t("auth.rateLimited"));
        else setError(t("errors.generic"));
      } else {
        setError(t("errors.generic"));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-svh w-full items-center justify-center bg-sidebar p-4">
      <div className="w-full max-w-md rounded-md border border-border bg-background p-5 shadow-sm">
        <div className="flex items-start justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
              <Cpu className="size-5" />
            </div>
            <div className="min-w-0">
              <h1 className="text-base font-semibold">{t("app.brand")}</h1>
              <p className="text-sm text-muted-foreground">{t("auth.description")}</p>
            </div>
          </div>
          <LoginPreferences />
        </div>

        <form className="mt-5 space-y-3" onSubmit={(e) => void onSubmit(e)}>
          <div>
            <Label className="mb-1.5" htmlFor="login-email">
              {t("auth.email")}
            </Label>
            <Input
              autoComplete="username"
              autoFocus
              className="mt-1.5"
              id="login-email"
              onChange={(e) => setEmail(e.target.value)}
              spellCheck={false}
              type="email"
              value={email}
            />
          </div>
          <div>
            <Label className="mb-1.5" htmlFor="login-password">
              {t("auth.password")}
            </Label>
            <Input
              autoComplete="current-password"
              className="mt-1.5"
              id="login-password"
              onChange={(e) => setPassword(e.target.value)}
              type="password"
              value={password}
            />
          </div>
          {error && (
            <p className="text-sm text-destructive" role="alert">
              {error}
            </p>
          )}
          <Button className="w-full" disabled={!canSubmit} type="submit">
            <LogIn className="size-4" />
            {t("auth.submit")}
          </Button>
        </form>
      </div>
    </div>
  );
}

// Tema e idioma também se escolhem na tela de login (fora da casca).
function LoginPreferences() {
  return (
    <div className="flex shrink-0 items-center gap-1.5">
      <LoginThemeMenu />
      <LoginLanguageMenu />
    </div>
  );
}

function LoginLanguageMenu() {
  const { locale, setLocale, t } = useI18n();
  const selected = LOCALE_OPTIONS.find((option) => option.value === locale) ?? LOCALE_OPTIONS[0];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={t("preferences.language")}
        className={cn(
          "inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground outline-none",
          "hover:bg-muted hover:text-foreground",
          "focus-visible:ring-2 focus-visible:ring-ring",
        )}
        type="button"
      >
        <span aria-hidden="true" className="text-xl leading-none">{selected.flag}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-max">
        <DropdownMenuRadioGroup onValueChange={(value) => setLocale(value as Locale)} value={locale}>
          {LOCALE_OPTIONS.map((option) => (
            <DropdownMenuRadioItem
              className="gap-2 whitespace-nowrap pr-8"
              key={option.value}
              value={option.value}
            >
              <span aria-hidden="true" className="text-lg leading-none">
                {option.flag}
              </span>
              <span className="pr-1">{option.label}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function LoginThemeMenu() {
  const { t } = useI18n();
  const { resolvedTheme, setTheme, theme } = useTheme();
  const options: Array<{ label: string; value: ThemePreference }> = [
    { label: t("preferences.theme.light"), value: "light" },
    { label: t("preferences.theme.dark"), value: "dark" },
    { label: t("preferences.theme.system"), value: "system" },
  ];
  const currentLabel = options.find((option) => option.value === theme)?.label ?? t("preferences.theme.system");
  const ThemeIcon = theme === "system" ? Monitor : resolvedTheme === "dark" ? Moon : Sun;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={`${t("preferences.theme")}: ${currentLabel}`}
        className={cn(
          "inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground outline-none",
          "hover:bg-muted hover:text-foreground",
          "focus-visible:ring-2 focus-visible:ring-ring",
        )}
        type="button"
      >
        <ThemeIcon className="size-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-24">
        <DropdownMenuRadioGroup onValueChange={(value) => setTheme(value as ThemePreference)} value={theme}>
          {options.map((option) => (
            <DropdownMenuRadioItem key={option.value} value={option.value}>
              {option.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
