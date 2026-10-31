import * as React from "react";
import { useQuery, type QueryClient } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { Cpu, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, isUnauthorized, setUnauthorizedHandler, type SessionUser } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

export const SESSION_QUERY_KEY = ["session"] as const;

/** Sessão do hub como query do TanStack Query (GET /api/session).
 *  401 → data ausente → a rota protegida redireciona para /login. */
export function useSession() {
  return useQuery({
    queryKey: SESSION_QUERY_KEY,
    queryFn: ({ signal }) => api.getSession(signal).then((session) => session.user),
    retry: false,
  });
}

/** O mínimo que o tratamento de 401 precisa do router; satisfazido pelo
 *  router real e pelos routers dos testes. */
type GuardRouter = {
  state: { location: { href: string; pathname: string } };
  navigate: (options: { replace?: boolean; search?: Record<string, unknown>; to: string }) => unknown;
};

/** Tratamento central de sessão perdida: todo 401 (menos o POST de login)
 *  remove a query da sessão do cache e leva o usuário para
 *  /login?next=<destino completo> (caminho + query + fragmento). Um 401 que
 *  chegou de uma conta que já não é a sessão vigente (request antigo de outra
 *  conta, cancelado no clear() do logout mas ainda resolvendo) é ignorado:
 *  limpar agora derrubaria a conta que acabou de entrar. Chamado uma vez
 *  pelo app (main.tsx) e pelos harnesses de teste. */
export function registerUnauthorizedRedirect(queryClient: QueryClient, router: GuardRouter): void {
  setUnauthorizedHandler((requestingUser) => {
    if (requestingUser !== undefined) {
      const current = queryClient.getQueryData<SessionUser>(SESSION_QUERY_KEY);
      if (current && current.id !== requestingUser) return;
    }
    // Limpa todos os dados autenticados (e não só a sessão): com a sessão
    // de outra conta, as telas da anterior nunca reaparecem.
    queryClient.clear();
    const { href, pathname } = router.state.location;
    if (pathname !== "/login") {
      // `next` guarda o destino interno inteiro para o login devolver o
      // usuário exatamente de onde ele saiu.
      router.navigate({ to: "/login", search: { next: safeNext(href) }, replace: true });
    }
  });
}

/** `next` do login só vale quando aponta para dentro do hub: começa com "/"
 *  e nunca com "//" (o que tornaria o valor protocolo-relative/absolute).
 *  Backslash e caractere de controle (U+0000–U+001F, U+007F) não pertencem a
 *  um caminho: nesses casos cai em "/" (o roteador codificaria controles no
 *  URL, mas o valor já não é um caminho interno confiável). Destinos
 *  completos (caminho + query + fragmento) passam por essas mesmas recusas. */
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
 *  /login?next=<destino completo>; carregando, mostra o estado de carga.
 *  Se a query falhar com erro diferente de 401 (500, rede), mostra o estado
 *  de erro com retry — sem redirecionar para o login, porque a causa não é
 *  credencial incorreta. */
export function RequireSession({ children }: { children: React.ReactNode }) {
  const { t } = useI18n();
  const { data, error, isError, isPending, refetch } = useSession();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const href = useRouterState({ select: (s) => s.location.href });
  const navigate = useNavigate();
  // Um 401 na query da sessão invalida o dado antigo: mesmo com o usuário
  // ainda no cache, a casca não aparece com a sessão expirada.
  const unauthorized = isUnauthorized(error);
  // Erro diferente de 401 é "sessão não verificada": a casca e os dados
  // antigos não aparecem; um erro transitório não é credencial incorreta.
  const checkFailed = isError && !unauthorized;
  const hasSession = data !== undefined && !unauthorized && !checkFailed;

  React.useEffect(() => {
    if (isPending || checkFailed) return;
    // Sem sessão e fora da tela de login: redireciona com o destino interno
    // completo (caminho + query + fragmento) como `next`. O guard de
    // pathname impede o efeito de rodar de novo após a própria navegação
    // (quando pathname já é "/login").
    if (!hasSession && pathname !== "/login") {
      navigate({ to: "/login", search: { next: safeNext(href) }, replace: true });
    }
  }, [checkFailed, hasSession, href, isPending, navigate, pathname]);

  if (isPending) {
    return <SessionFallback message={t("auth.loadingSession")} />;
  }
  if (checkFailed) {
    return <SessionCheckFailed onRetry={() => void refetch()} />;
  }
  if (!hasSession) {
    return <SessionFallback message={t("auth.loadingSession")} />;
  }
  return <>{children}</>;
}

/** Placeholder centralizado enquanto o estado da sessão é decidido. */
function SessionFallback({ message }: { message: string }) {
  return (
    <div className="flex min-h-svh items-center justify-center text-sm text-muted-foreground">
      {message}
    </div>
  );
}

/** Sessão não verificada por erro diferente de 401: tela simples (marca +
 *  mensagem) com retry que refaz a query. Sem redirect para o login: a
 *  causa não é credencial incorreta. */
function SessionCheckFailed({ onRetry }: { onRetry: () => void }) {
  const { t } = useI18n();

  return (
    <div className="flex min-h-svh w-full items-center justify-center bg-sidebar p-4">
      <div className="flex w-full max-w-md flex-col items-center gap-4 rounded-md border border-border bg-background p-5 text-center shadow-sm">
        <div className="flex size-10 items-center justify-center rounded-md bg-primary/10 text-primary">
          <Cpu className="size-5" />
        </div>
        <div className="space-y-1">
          <p className="text-base font-semibold">{t("app.brand")}</p>
          <p className="text-sm text-muted-foreground" role="alert">
            {t("auth.sessionCheckFailed")}
          </p>
        </div>
        <Button onClick={onRetry}>
          <RefreshCw className="size-4" />
          {t("auth.sessionRetry")}
        </Button>
      </div>
    </div>
  );
}

/** Nome de exibição do usuário da sessão. */
export function sessionName(user: SessionUser): string {
  return user.name || user.email;
}
