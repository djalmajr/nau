import * as React from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Cpu, UserPlus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { SESSION_QUERY_KEY } from "@/lib/session";

/** Aceitar convite: rota pública, sem a casca (como a tela Entrar). O token
 *  vem do fragmento da URL (`/invite#<token>`), nunca da query: lido uma vez
 *  no mount e limpo da URL na hora; não vai para log, `localStorage` nem
 *  estado que sobreviva à renderização. */
export function InviteScreen() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  // O token é lido uma única vez (do fragmento) e a URL fica limpa: um
  // recarregamento ou navegação de volta não expõe o token de novo.
  const tokenRef = React.useRef<string | null>(null);
  if (tokenRef.current === null) {
    tokenRef.current = readToken();
  }
  const token = tokenRef.current ?? "";

  const [name, setName] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");

  const passwordOk = password.length >= 12 && password.length <= 128;
  const canSubmit = name.trim().length > 0 && passwordOk && !busy;

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit || !token) return;
    setBusy(true);
    setError("");
    try {
      const res = await api.acceptInvite(token, name.trim(), password);
      // Limpa qualquer dado de sessão anterior e entra na casca, como o
      // login faz.
      queryClient.clear();
      queryClient.setQueryData(SESSION_QUERY_KEY, res.user);
      navigate({ replace: true, to: "/" });
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 400) setError(t("invite.invalidOrExpired"));
        else if (err.status === 409) setError(t("invite.emailTaken"));
        else setError(t("errors.generic"));
      } else {
        setError(t("errors.generic"));
      }
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-svh w-full items-center justify-center bg-sidebar p-4">
      <div className="w-full max-w-md rounded-md border border-border bg-background p-5 shadow-sm">
        <div className="flex items-start gap-3">
          <div className="flex size-10 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
            <Cpu className="size-5" />
          </div>
          <div className="min-w-0">
            <h1 className="text-base font-semibold">{t("invite.title")}</h1>
            <p className="text-sm text-muted-foreground">{t("invite.description")}</p>
          </div>
        </div>

        {!token ? (
          <p className="mt-5 text-sm text-destructive" role="alert">
            {t("invite.invalidOrExpired")}
          </p>
        ) : (
          <form className="mt-5 space-y-3" onSubmit={(e) => void onSubmit(e)}>
            <div>
              <Label className="mb-1.5" htmlFor="invite-name">
                {t("invite.name")}
              </Label>
              <Input
                autoComplete="name"
                className="mt-1.5"
                id="invite-name"
                onChange={(e) => setName(e.target.value)}
                value={name}
              />
            </div>
            <div>
              <Label className="mb-1.5" htmlFor="invite-password">
                {t("invite.password")}
              </Label>
              <Input
                autoComplete="new-password"
                className="mt-1.5"
                id="invite-password"
                onChange={(e) => setPassword(e.target.value)}
                type="password"
                value={password}
              />
              <p className="mt-1 text-xs text-muted-foreground">{t("invite.passwordHint")}</p>
            </div>
            {error ? (
              <p className="text-sm text-destructive" role="alert">
                {error}
              </p>
            ) : null}
            <Button className="w-full" disabled={!canSubmit} type="submit">
              <UserPlus className="size-4" />
              {t("invite.submit")}
            </Button>
          </form>
        )}
      </div>
    </div>
  );
}

/** Lê o token do fragmento da URL atual e limpa o fragmento (o token nunca
 *  fica na URL depois). Sem fragmento: vazio (convite inválido). */
function readToken(): string {
  const { hash, pathname, search } = window.location;
  const token = hash.startsWith("#") ? hash.slice(1) : "";
  if (hash) {
    try {
      window.history.replaceState(null, "", pathname + search);
    } catch {
      // contexto sem history (raro): o token segue no fragmento e o aceite
      // continua válido; a limpeza só é melhor.
    }
  }
  return token;
}
