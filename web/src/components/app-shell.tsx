import * as React from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Bot,
  Cpu,
  FolderKanban,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Monitor,
  Moon,
  ServerCog,
  Settings2,
  ShieldCheck,
  Sun,
  Users,
  type LucideIcon,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { ScrollArea } from "@/components/ui/scroll-area";
import { SimpleTooltip } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import { avatarHue, initials } from "@/lib/avatar";
import { LOCALE_OPTIONS, useI18n, type Locale, type TranslationKey } from "@/lib/i18n";
import { useLiveStatus } from "@/lib/live-status";
import { sessionName, useSession } from "@/lib/session";
import { type ThemePreference, useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

type NavItem = { exact?: boolean; icon: LucideIcon; labelKey: TranslationKey; to: string };

const NAV_GROUPS: Array<{ labelKey: TranslationKey; items: NavItem[] }> = [
  {
    labelKey: "nav.group.views",
    items: [
      { exact: true, icon: LayoutDashboard, labelKey: "nav.overview", to: "/" },
      { icon: Bot, labelKey: "nav.agents", to: "/agents" },
      { icon: ServerCog, labelKey: "nav.machines", to: "/machines" },
      { icon: Users, labelKey: "nav.developers", to: "/developers" },
    ],
  },
  {
    labelKey: "nav.group.projects",
    items: [
      { icon: FolderKanban, labelKey: "nav.projects", to: "/projects" },
      { icon: ShieldCheck, labelKey: "nav.privacy", to: "/privacy" },
    ],
  },
  {
    labelKey: "nav.group.system",
    items: [
      { icon: KeyRound, labelKey: "nav.access", to: "/access" },
      { icon: Settings2, labelKey: "nav.settings", to: "/settings" },
    ],
  },
];

export function AppShell({
  children,
  headerActions,
}: {
  children: React.ReactNode;
  /** Ações da página à direita do título no header (ex.: troca Board | Lista).
   *  Abaixo de 640 px saem da linha do header e vão para a linha própria
   *  logo abaixo, antes dos filtros. */
  headerActions?: React.ReactNode;
}) {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const narrow = useIsNarrow();

  return (
    <SidebarProvider>
      <Sidebar
        mobileDescription={t("sidebar.mobile.description")}
        mobileTitle={t("sidebar.mobile.title")}
      >
        <SidebarHeader>
          <div className="flex items-center gap-2 px-1 py-1.5">
            <div className="flex size-8 shrink-0 items-center justify-center">
              <Cpu className="size-[22px]" />
            </div>
            <div className="flex flex-col group-data-[state=collapsed]:hidden">
              <span className="text-sm font-semibold leading-tight">{t("app.brand")}</span>
            </div>
          </div>
        </SidebarHeader>

        <SidebarContent>
          {NAV_GROUPS.map((group) => (
            <SidebarGroup key={group.labelKey}>
              <SidebarGroupLabel>{t(group.labelKey)}</SidebarGroupLabel>
              <SidebarMenu>
                {group.items.map((item) => {
                  const active = item.exact ? pathname === item.to : pathname.startsWith(item.to);
                  const Icon = item.icon;
                  const label = t(item.labelKey);
                  return (
                    <SidebarMenuItem key={item.to}>
                      <SidebarMenuButton
                        isActive={active}
                        tooltip={label}
                        render={<Link aria-label={label} to={item.to} />}
                      >
                        <Icon />
                        <span className="group-data-[state=collapsed]:hidden">{label}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroup>
          ))}
        </SidebarContent>
      </Sidebar>

      {/* h-svh + overflow-hidden: o header fica fixo e SÓ o main rola. Abaixo
          de 640 px a ação da página sai da linha do header (vai para a linha
          própria logo abaixo, antes dos filtros); os controles globais
          (pílula, tema, idioma e conta) ficam sempre na linha de cima,
          visíveis até 320 px, e o título truncando impede overflow. */}
      <SidebarInset className="h-svh overflow-hidden">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
          <SidebarTrigger label={t("sidebar.toggle")} />
          <div className="flex min-w-0 flex-col">
            <div className="flex min-w-0 items-center gap-1">
              <span className="truncate text-sm font-medium leading-tight">
                {pageTitle(pathname, t)}
              </span>
            </div>
            {pageDescription(pathname, t) && (
              <span className="truncate text-xs leading-tight text-muted-foreground">
                {pageDescription(pathname, t)}
              </span>
            )}
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {!narrow ? headerActions : null}
            <div className="flex items-center gap-1.5">
              <LiveStatusPill />
              <ThemeMenu />
              <LanguageMenu />
              <UserMenu />
            </div>
          </div>
        </header>
        {narrow && headerActions ? (
          <div className="flex shrink-0 items-center gap-2 border-b px-4 py-2">
            {headerActions}
          </div>
        ) : null}
        <ScrollArea className="min-h-0 flex-1">
          <main className="p-4">{children}</main>
        </ScrollArea>
      </SidebarInset>
    </SidebarProvider>
  );
}

/** Abaixo do breakpoint `sm` (640 px) do Tailwind: a ação da página sai da
 *  linha do header. O stub de matchMedia dos testes emula o viewport de 1024 px
 *  do jsdom (`min-width: 640px` casa), então os testes veem o header largo.
 */
function useIsNarrow(): boolean {
  const [narrow, setNarrow] = React.useState(false);
  React.useEffect(() => {
    const media = window.matchMedia("(min-width: 640px)");
    const update = () => setNarrow(!media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  return narrow;
}

/** Pílula de conexão em tempo real. Nesta base o hook sempre devolve
 *  "offline"; o canal ao vivo chega num card seguinte. Abaixo de 640 px
 *  compacta para só o ponto (o rótulo fica na árvore acessível, via
 *  sr-only), para o header caber em 320 px sem esconder os demais controles. */
function LiveStatusPill() {
  const { t } = useI18n();
  const status = useLiveStatus();
  const label = t(`live.status.${status}` as TranslationKey);

  return (
    <Badge
      className={cn(
        "shrink-0",
        status === "live" && "border-transparent bg-primary/15 text-primary",
        status === "reconnecting" && "border-transparent bg-secondary text-secondary-foreground",
        status === "offline" && "text-muted-foreground",
      )}
      variant="outline"
    >
      <span
        aria-hidden
        className={cn(
          "size-1.5 rounded-full",
          status === "live" && "bg-primary",
          status === "reconnecting" && "bg-secondary-foreground/60",
          status === "offline" && "bg-muted-foreground/50",
        )}
      />
      <span className="hidden sm:inline">{label}</span>
      <span className="sr-only sm:hidden">{label}</span>
    </Badge>
  );
}

// O nome do usuário vem da mesma query de sessão das rotas protegidas; o
// RequireSession já garantiu que a sessão existe quando o shell é montado.
function UserMenu() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: user } = useSession();

  async function logout() {
    try {
      await api.logout();
    } catch {
      // mesmo com falha no DELETE, encerra a sessão localmente
    }
    // Limpa todos os dados autenticados (e não só a sessão): os dados de
    // outra conta nunca reaparecem no navegador depois do logout.
    queryClient.clear();
    navigate({ to: "/login", replace: true });
  }

  if (!user) return null;
  const displayName = sessionName(user);

  return (
    <DropdownMenu>
      <SimpleTooltip label={displayName}>
        <DropdownMenuTrigger
          aria-label={t("nav.account")}
          className={cn(
            "inline-flex size-8 shrink-0 items-center justify-center overflow-hidden rounded-full outline-none",
            "transition-opacity hover:opacity-90",
            "focus-visible:ring-2 focus-visible:ring-ring",
          )}
          type="button"
        >
          <span
            aria-hidden
            className="flex size-8 items-center justify-center rounded-full text-[11px] font-semibold leading-none text-white"
            style={{ background: avatarHue(displayName) }}
          >
            {initials(displayName)}
          </span>
        </DropdownMenuTrigger>
      </SimpleTooltip>
      <DropdownMenuContent align="end" className="min-w-48">
        <DropdownMenuGroup>
          <DropdownMenuLabel className="font-normal">
            <span className="block truncate text-sm font-medium leading-tight">{displayName}</span>
          </DropdownMenuLabel>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem onClick={() => void logout()}>
            <LogOut />
            {t("nav.logout")}
          </DropdownMenuItem>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function LanguageMenu() {
  const { locale, setLocale, t } = useI18n();
  const selected = LOCALE_OPTIONS.find((option) => option.value === locale) ?? LOCALE_OPTIONS[0];

  return (
    <DropdownMenu>
      <SimpleTooltip label={t("preferences.language")}>
        <DropdownMenuTrigger
          aria-label={t("preferences.language")}
          className={cn(buttonVariants({ size: "icon-sm", variant: "ghost" }))}
          type="button"
        >
          <span aria-hidden="true" className="text-xl leading-none">{selected.flag}</span>
        </DropdownMenuTrigger>
      </SimpleTooltip>
      <DropdownMenuContent className="min-w-max">
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

export function ThemeMenu() {
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
      <SimpleTooltip label={`${t("preferences.theme")}: ${currentLabel}`}>
        <DropdownMenuTrigger
          aria-label={`${t("preferences.theme")}: ${currentLabel}`}
          className={cn(buttonVariants({ size: "icon-sm", variant: "ghost" }))}
          type="button"
        >
          <ThemeIcon className="size-4" />
        </DropdownMenuTrigger>
      </SimpleTooltip>
      <DropdownMenuContent className="min-w-24">
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

function pageTitle(pathname: string, t: (key: TranslationKey) => string): string {
  if (pathname === "/") return t("nav.overview");
  if (pathname.startsWith("/agents")) return t("nav.agents");
  if (pathname.startsWith("/machines")) return t("nav.machines");
  if (pathname.startsWith("/developers")) return t("nav.developers");
  if (pathname.startsWith("/projects")) return t("nav.projects");
  if (pathname.startsWith("/privacy")) return t("nav.privacy");
  if (pathname.startsWith("/access")) return t("nav.access");
  if (pathname.startsWith("/settings")) return t("nav.settings");
  return t("notFound.title");
}

// Descrição da página no header — acompanha o título de cada rota.
function pageDescription(pathname: string, t: (key: TranslationKey) => string): string {
  if (pathname === "/") return t("overview.description");
  if (pathname.startsWith("/agents")) return t("agents.description");
  if (pathname.startsWith("/machines")) return t("machines.description");
  if (pathname.startsWith("/developers")) return t("developers.description");
  if (pathname.startsWith("/projects")) return t("projects.description");
  if (pathname.startsWith("/privacy")) return t("privacy.description");
  if (pathname.startsWith("/access")) return t("access.description");
  if (pathname.startsWith("/settings")) return t("settings.description");
  return t("notFound.description");
}
