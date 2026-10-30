import * as React from "react";
import { useQuery, type QueryClient } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { api, isUnauthorized, setUnauthorizedHandler, type SessionUser } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

export const SESSION_QUERY_KEY = ["session"] as const;

/** Sessão do hub como query do TanStack Query (GET /api/session).
 *  401 → data ausente → a rota protegida redireciona para /login. */
export function useSession() {
  return useQuery({
    queryKey: SESSION_QUERY_KEY,
    queryFn: async () => (await api.getSession()).user,
    retry: false,
  });
}

/** O mínimo que o tratamento de 401 precisa do router; satisfazido pelo
 *  router real e pelos routers dos testes. */
type GuardRouter = {
  state: { location: { pathname: string } };
  navigate: (options: { replace?: boolean; search?: Record<string, unknown>; to: string }) => unknown;
};

/** Tratamento central de sessão perdida: todo 401 (menos o POST de login)
 *  remove a query da sessão do cache e leva o usuário para
 *  /login?next=<caminho atual>. Chamado uma vez pelo app (main.tsx) e pelos
 *  harnesses de teste. */
export function registerUnauthorizedRedirect(queryClient: QueryClient, router: GuardRouter): void {
  setUnauthorizedHandler(() => {
    queryClient.removeQueries({ queryKey: SESSION_QUERY_KEY });
    const { pathname } = router.state.location;
    if (pathname !== "/login") {
      router.navigate({ to: "/login", search: { next: pathname }, replace: true });
    }
  });
}

/** `next` do login só vale quando aponta para dentro do hub: começa com "/"
 *  e nunca com "//" (o que tornaria o valor protocolo-relative/absolute).
 *  Backslash e caractere de controle (U+0000–U+001F, U+007F) não pertencem a
 *  um caminho: nesses casos cai em "/" (o roteador codificaria controles no
 *  URL, mas o valor já não é um caminho interno confiável). */
export function safeNext(next: string | undefined): string {
  if (typeof next !== "string") return "/";
  if (next.startsWith("//")) return "/";
  if (!next.startsWith("/")) return "/";
  if (next.includes("\\")) return "/";
  for (let index = 0; index < next.length; index += 1) {
    const code = next.charCodeAt(index);
    if (code <= 0x1f || code === 0x7f) return "/";
  }
  return next;
}

/** Toda rota, menos /login, exige sessão. Sem sessão, redireciona para
 *  /login?next=<caminho>; carregando, mostra o estado de carga. */
export function RequireSession({ children }: { children: React.ReactNode }) {
  const { t } = useI18n();
  const { data, error, isPending } = useSession();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  // Um 401 na query da sessão invalida o dado antigo: mesmo com o usuário
  // ainda no cache, a casca não aparece com a sessão expirada.
  const hasSession = data !== undefined && !isUnauthorized(error);

  React.useEffect(() => {
    if (isPending) return;
    // Sem sessão e fora da tela de login: redireciona com o caminho atual
    // como `next`. O guard de pathname impede o efeito de rodar de novo após
    // a própria navegação (quando pathname já é "/login").
    if (!hasSession && pathname !== "/login") {
      navigate({ to: "/login", search: { next: pathname }, replace: true });
    }
  }, [hasSession, isPending, navigate, pathname]);

  if (isPending || !hasSession) {
    return (
      <div className="flex min-h-svh items-center justify-center text-sm text-muted-foreground">
        {t("auth.loadingSession")}
      </div>
    );
  }
  return <>{children}</>;
}

/** Nome de exibição do usuário da sessão. */
export function sessionName(user: SessionUser): string {
  return user.name || user.email;
}
