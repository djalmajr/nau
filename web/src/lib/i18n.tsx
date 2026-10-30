import * as React from "react";

export const LOCALE_STORAGE_KEY = "nauLocale";

export const LOCALE_OPTIONS = [
  { flag: "🇧🇷", label: "Português", value: "pt-BR" },
  { flag: "🇺🇸", label: "English", value: "en-US" },
] as const;

const DEFAULT_LOCALE = "pt-BR";

const ptBR = {
  "app.brand": "Nau",
  "nav.group.views": "Visão",
  "nav.group.projects": "Projetos",
  "nav.group.system": "Sistema",
  "nav.overview": "Visão geral",
  "nav.agents": "Agentes",
  "nav.machines": "Máquinas",
  "nav.developers": "Desenvolvedores",
  "nav.projects": "Projetos",
  "nav.privacy": "Privacidade",
  "nav.access": "Acesso",
  "nav.settings": "Configurações",
  "nav.account": "Conta",
  "nav.logout": "Sair",
  "sidebar.toggle": "Mostrar ou ocultar a barra lateral",
  "sidebar.mobile.title": "Barra lateral",
  "sidebar.mobile.description": "Mostra a barra lateral em telas estreitas.",
  "overview.description": "Agentes, máquinas e atividade do hub.",
  "agents.description": "Todos os agentes registrados no hub.",
  "machines.description": "Máquinas conectadas e seu estado.",
  "developers.description": "Desenvolvedores e suas contas.",
  "projects.description": "Projetos e os agentes que participam deles.",
  "privacy.description": "O que o hub guarda e como sair dele.",
  "access.description": "Contas, papéis e sessões de acesso.",
  "settings.description": "Preferências e configurações da sua conta.",
  "pages.empty": "Nada por aqui ainda",
  "errors.generic": "Algo deu errado. Tente novamente.",
  "notFound.title": "Página não encontrada",
  "notFound.description": "O endereço não existe ou mudou.",
  "notFound.back": "Voltar para a visão geral",
  "auth.title": "Entrar",
  "auth.description": "Entre com sua conta para acessar o hub.",
  "auth.email": "E-mail",
  "auth.password": "Senha",
  "auth.submit": "Entrar",
  "auth.invalidCredentials": "E-mail ou senha incorretos",
  "auth.rateLimited": "Muitas tentativas. Tente de novo em instantes.",
  "auth.loadingSession": "Carregando sessão…",
  "live.status.live": "Ao vivo",
  "live.status.reconnecting": "Reconectando",
  "live.status.offline": "Offline",
  "preferences.language": "Idioma",
  "preferences.theme": "Tema",
  "preferences.theme.light": "Claro",
  "preferences.theme.dark": "Escuro",
  "preferences.theme.system": "Sistema",
} as const;

export type Locale = (typeof LOCALE_OPTIONS)[number]["value"];
export type TranslationKey = keyof typeof ptBR;

type InterpolationParams = Record<string, number | string>;

const enUS: Record<TranslationKey, string> = {
  "app.brand": "Nau",
  "nav.group.views": "Views",
  "nav.group.projects": "Projects",
  "nav.group.system": "System",
  "nav.overview": "Overview",
  "nav.agents": "Agents",
  "nav.machines": "Machines",
  "nav.developers": "Developers",
  "nav.projects": "Projects",
  "nav.privacy": "Privacy",
  "nav.access": "Access",
  "nav.settings": "Settings",
  "nav.account": "Account",
  "nav.logout": "Sign out",
  "sidebar.toggle": "Toggle Sidebar",
  "sidebar.mobile.title": "Sidebar",
  "sidebar.mobile.description": "Displays the mobile sidebar.",
  "overview.description": "Agents, machines and hub activity.",
  "agents.description": "All agents registered in the hub.",
  "machines.description": "Connected machines and their status.",
  "developers.description": "Developers and their accounts.",
  "projects.description": "Projects and the agents taking part in them.",
  "privacy.description": "What the hub stores and how to leave it.",
  "access.description": "Accounts, roles and access sessions.",
  "settings.description": "Preferences and settings for your account.",
  "pages.empty": "Nothing here yet",
  "errors.generic": "Something went wrong. Try again.",
  "notFound.title": "Page not found",
  "notFound.description": "The address does not exist or has changed.",
  "notFound.back": "Back to overview",
  "auth.title": "Sign in",
  "auth.description": "Sign in with your account to access the hub.",
  "auth.email": "Email",
  "auth.password": "Password",
  "auth.submit": "Sign in",
  "auth.invalidCredentials": "Invalid email or password",
  "auth.rateLimited": "Too many attempts. Try again in a moment.",
  "auth.loadingSession": "Loading session…",
  "live.status.live": "Live",
  "live.status.reconnecting": "Reconnecting",
  "live.status.offline": "Offline",
  "preferences.language": "Language",
  "preferences.theme": "Theme",
  "preferences.theme.light": "Light",
  "preferences.theme.dark": "Dark",
  "preferences.theme.system": "System",
};

const MESSAGES: Record<Locale, Record<TranslationKey, string>> = {
  "en-US": enUS,
  "pt-BR": ptBR,
};

type I18nContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: TranslationKey, params?: InterpolationParams) => string;
};

const I18nContext = React.createContext<I18nContextValue | null>(null);

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = React.useState(readStoredLocale);

  React.useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = React.useCallback((next: Locale) => {
    setLocaleState(next);
    localStorage.setItem(LOCALE_STORAGE_KEY, next);
  }, []);

  const t = React.useCallback(
    (key: TranslationKey, params?: InterpolationParams) => translate(locale, key, params),
    [locale],
  );

  const value = React.useMemo(
    () => ({ locale, setLocale, t }),
    [locale, setLocale, t],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function normalizeLocale(value: string | null | undefined): Locale {
  const normalized = value?.replace("_", "-").toLowerCase();
  if (normalized === "en" || normalized === "en-us") return "en-US";
  if (normalized === "pt" || normalized === "pt-br") return "pt-BR";
  return DEFAULT_LOCALE;
}

export function translate(locale: Locale, key: TranslationKey, params: InterpolationParams = {}): string {
  const template = MESSAGES[locale][key] ?? ptBR[key];
  return template.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ""));
}

export function useI18n() {
  const context = React.useContext(I18nContext);
  if (!context) throw new Error("useI18n must be used within <I18nProvider>");
  return context;
}

function readStoredLocale(): Locale {
  if (typeof window === "undefined") return DEFAULT_LOCALE;
  return normalizeLocale(localStorage.getItem(LOCALE_STORAGE_KEY));
}
