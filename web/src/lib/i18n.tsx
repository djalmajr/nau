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
  "errors.retry": "Tentar de novo",
  "loading": "Carregando…",
  "states.needs_you": "Precisa de você",
  "states.working": "Trabalhando",
  "states.done": "Concluído",
  "states.idle": "Ocioso",
  "states.unknown": "Desconhecido",
  "reasons.question": "Pergunta",
  "reasons.approval": "Aprovação",
  "reasons.quota": "Quota",
  "reasons.provider": "Provedor",
  "reasons.not_received": "Não recebido",
  "reasons.no_report": "Sem relatório",
  "reasons.compact": "Compactação",
  "time.justNow": "agora",
  "machine.online": "online",
  "machine.offline": "offline",
  "report.ready": "Relatório pronto",
  "agents.view.board": "Board",
  "agents.view.list": "Lista",
  "agents.empty": "Nenhum agente registrado no hub.",
  "agents.emptyFiltered": "Nenhum agente com os filtros atuais.",
  "filters.all": "Todos",
  "filters.developer": "Desenvolvedor",
  "filters.machine": "Máquina",
  "filters.project": "Projeto",
  "filters.search": "Buscar por tarefa ou nome do agente",
  "board.hiddenCount": "{n} em projetos ocultos",
  "board.unknownTitle": "Estado desconhecido",
  "agents.column.state": "Estado",
  "agents.column.agent": "Agente",
  "agents.column.task": "Tarefa",
  "agents.column.project": "Projeto",
  "agents.column.machine": "Máquina",
  "agents.column.developer": "Desenvolvedor",
  "agents.column.assistant": "Assistente",
  "agents.column.since": "Desde",
  "agents.back": "Agentes",
  "list.hiddenRow": "{n} agentes em projetos ocultos",
  "detail.state": "Estado",
  "detail.assistant": "Assistente",
  "detail.model": "Modelo",
  "detail.effort": "Esforço",
  "detail.project": "Projeto",
  "detail.session": "Sessão",
  "detail.terminal": "Terminal",
  "detail.machine": "Máquina",
  "detail.developer": "Desenvolvedor",
  "detail.started": "Iniciado",
  "detail.since": "No estado desde",
  "detail.observed": "Observado",
  "detail.reportReady": "Relatório pronto",
  "detail.yes": "Sim",
  "detail.no": "Não",
  "detail.history": "Histórico",
  "detail.historyEmpty": "Sem mudanças nos últimos 7 dias",
  "detail.historyNoReason": "—",
  "detail.notFound": "Agente não encontrado",
  "detail.notFoundDescription": "O agente não existe ou a sessão da máquina terminou.",
  "detail.privacyNote": "O que nunca sai da máquina: brief, relatório, tela, caminhos, ambiente e saída de comandos.",
  "overview.needsYou": "Precisa de você",
  "overview.working": "Trabalhando",
  "overview.done": "Concluído",
  "overview.idle": "Ocioso",
  "overview.machinesOnline": "Máquinas online",
  "overview.developers": "Desenvolvedores",
  "overview.machinesOnlineValue": "{online} de {total}",
  "overview.waiting": "Esperando por você agora",
  "overview.waitingEmpty": "Ninguém esperando por você",
  "overview.perMachine": "Agentes por máquina",
  "overview.perMachineEmpty": "Nenhuma máquina conectada.",
  "overview.working24h": "Trabalhando nas últimas 24 h",
  "overview.working24hBar": "{hour}: {count} trabalhando",
  "overview.topProjects": "Projetos mais ativos",
  "overview.topProjectsEmpty": "Nenhum projeto ativo.",
  "overview.topProjectsAgents": "agentes",
  "overview.topProjectsWorking": "trabalhando",
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
  "auth.sessionCheckFailed": "Não foi possível verificar a sua sessão",
  "auth.sessionRetry": "Tentar de novo",
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
  "errors.retry": "Try again",
  "loading": "Loading…",
  "states.needs_you": "Needs you",
  "states.working": "Working",
  "states.done": "Done",
  "states.idle": "Idle",
  "states.unknown": "Unknown",
  "reasons.question": "Question",
  "reasons.approval": "Approval",
  "reasons.quota": "Quota",
  "reasons.provider": "Provider",
  "reasons.not_received": "Not received",
  "reasons.no_report": "No report",
  "reasons.compact": "Compaction",
  "time.justNow": "just now",
  "machine.online": "online",
  "machine.offline": "offline",
  "report.ready": "Report ready",
  "agents.view.board": "Board",
  "agents.view.list": "List",
  "agents.empty": "No agents registered in the hub.",
  "agents.emptyFiltered": "No agents match the current filters.",
  "filters.all": "All",
  "filters.developer": "Developer",
  "filters.machine": "Machine",
  "filters.project": "Project",
  "filters.search": "Search by task or agent name",
  "board.hiddenCount": "{n} in hidden projects",
  "board.unknownTitle": "Unknown state",
  "agents.column.state": "State",
  "agents.column.agent": "Agent",
  "agents.column.task": "Task",
  "agents.column.project": "Project",
  "agents.column.machine": "Machine",
  "agents.column.developer": "Developer",
  "agents.column.assistant": "Assistant",
  "agents.column.since": "Since",
  "agents.back": "Agents",
  "list.hiddenRow": "{n} agents in hidden projects",
  "detail.state": "State",
  "detail.assistant": "Assistant",
  "detail.model": "Model",
  "detail.effort": "Effort",
  "detail.project": "Project",
  "detail.session": "Session",
  "detail.terminal": "Terminal",
  "detail.machine": "Machine",
  "detail.developer": "Developer",
  "detail.started": "Started",
  "detail.since": "In state since",
  "detail.observed": "Observed",
  "detail.reportReady": "Report ready",
  "detail.yes": "Yes",
  "detail.no": "No",
  "detail.history": "History",
  "detail.historyEmpty": "No changes in the last 7 days",
  "detail.historyNoReason": "—",
  "detail.notFound": "Agent not found",
  "detail.notFoundDescription": "The agent does not exist or the machine session has ended.",
  "detail.privacyNote": "What never leaves the machine: brief, report, screen, paths, environment and command output.",
  "overview.needsYou": "Needs you",
  "overview.working": "Working",
  "overview.done": "Done",
  "overview.idle": "Idle",
  "overview.machinesOnline": "Machines online",
  "overview.developers": "Developers",
  "overview.machinesOnlineValue": "{online} of {total}",
  "overview.waiting": "Waiting for you now",
  "overview.waitingEmpty": "Nobody is waiting for you",
  "overview.perMachine": "Agents by machine",
  "overview.perMachineEmpty": "No machines connected.",
  "overview.working24h": "Working in the last 24 h",
  "overview.working24hBar": "{hour}: {count} working",
  "overview.topProjects": "Most active projects",
  "overview.topProjectsEmpty": "No active projects.",
  "overview.topProjectsAgents": "agents",
  "overview.topProjectsWorking": "working",
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
  "auth.sessionCheckFailed": "Could not check your session",
  "auth.sessionRetry": "Try again",
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
  // Fallback em duas camadas: en-US ausente volta ao pt-BR; chave inexistente
  // vira a própria chave (nunca undefined — a UI não quebra por typo de chave).
  const template = MESSAGES[locale][key] ?? ptBR[key] ?? String(key);
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
