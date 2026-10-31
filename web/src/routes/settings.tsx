import * as React from "react";
import { createRoute } from "@tanstack/react-router";
import { AppShell, LanguageMenu, ThemeMenu } from "@/components/app-shell";
import { PageLoading } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useChangePassword, useVersion } from "@/lib/queries";
import { RequireSession, sessionName, useSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: SettingsPage,
  getParentRoute: () => rootRoute,
  path: "/settings",
});

/** Configurações: conta (leitura), troca de senha, aparência (os mesmos
 *  seletores do cabeçalho) e o hub (versão, leitura). */
export function SettingsPage() {
  const { t } = useI18n();
  const session = useSession();

  if (session.isPending || !session.data) {
    return (
      <RequireSession>
        <AppShell>
          <PageLoading />
        </AppShell>
      </RequireSession>
    );
  }

  return (
    <RequireSession>
      <AppShell>
        <div className="max-w-2xl space-y-8">
          <AccountCard name={sessionName(session.data)} email={session.data.email} />
          <PasswordCard />
          <Card>
            <CardHeader>
              <CardTitle>{t("settings.appearance.title")}</CardTitle>
            </CardHeader>
            <CardContent className="flex items-center gap-2">
              <ThemeMenu />
              <LanguageMenu />
            </CardContent>
          </Card>
          <HubCard />
        </div>
      </AppShell>
    </RequireSession>
  );
}

function AccountCard({ email, name }: { email: string; name: string }) {
  const { t } = useI18n();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.account.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        <div className="flex items-baseline justify-between gap-2">
          <span className="text-xs text-muted-foreground">{t("settings.account.name")}</span>
          <span className="min-w-0 truncate font-medium">{name}</span>
        </div>
        <div className="flex items-baseline justify-between gap-2">
          <span className="text-xs text-muted-foreground">{t("settings.account.email")}</span>
          <span className="min-w-0 truncate font-medium">{email}</span>
        </div>
      </CardContent>
    </Card>
  );
}

/** Troca de senha (POST /api/session/password): 401 = senha atual errada;
 *  400 weak_password = comprimento fora de 12–128. As senhas vivem só no
 *  estado local do formulário (não vão para log, URL nem armazenamento). */
function PasswordCard() {
  const { t } = useI18n();
  const [current, setCurrent] = React.useState("");
  const [next, setNext] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const [success, setSuccess] = React.useState(false);
  const change = useChangePassword();

  const canSubmit = current.length > 0 && next.length > 0 && confirm.length > 0 && !busy;

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit) return;
    setError("");
    setSuccess(false);
    // Validação local antes de gastar a chamada: as duas novas conferem e
    // já têm o comprimento certo (a regra do servidor é a mesma).
    if (next !== confirm) {
      setError(t("settings.password.mismatch"));
      return;
    }
    if (next.length < 12 || next.length > 128) {
      setError(t("settings.password.weak"));
      return;
    }
    setBusy(true);
    try {
      await change.mutateAsync({ current, newPassword: next });
      setSuccess(true);
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) setError(t("settings.password.wrongCurrent"));
        else if (err.status === 400 && err.serverError === "weak_password") setError(t("settings.password.weak"));
        else setError(t("errors.generic"));
      } else {
        setError(t("errors.generic"));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.password.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <form className="space-y-3" noValidate onSubmit={(event) => void onSubmit(event)}>
          <div className="space-y-1.5">
            <Label htmlFor="password-current">{t("settings.password.current")}</Label>
            <Input
              autoComplete="current-password"
              id="password-current"
              onChange={(event) => setCurrent(event.target.value)}
              type="password"
              value={current}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="password-new">{t("settings.password.new")}</Label>
            <Input
              autoComplete="new-password"
              id="password-new"
              onChange={(event) => setNext(event.target.value)}
              type="password"
              value={next}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="password-confirm">{t("settings.password.confirm")}</Label>
            <Input
              autoComplete="new-password"
              id="password-confirm"
              onChange={(event) => setConfirm(event.target.value)}
              type="password"
              value={confirm}
            />
          </div>
          {error ? (
            <p className="text-sm text-destructive" role="alert">
              {error}
            </p>
          ) : null}
          {success ? (
            <p className="text-sm" role="status">
              {t("settings.password.success")}
            </p>
          ) : null}
          <Button disabled={!canSubmit} type="submit">
            {t("settings.password.submit")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

/** Hub: versão (GET /api/version), só para leitura. */
function HubCard() {
  const { t } = useI18n();
  const { data, error, isPending, refetch } = useVersion();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.hub.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        <div className="flex items-baseline justify-between gap-2">
          <span className="text-xs text-muted-foreground">{t("settings.hub.version")}</span>
          {isPending ? (
            <span className="text-muted-foreground">…</span>
          ) : error ? (
            <span className="flex items-center gap-2 text-muted-foreground">
              {t("errors.generic")}
              <Button onClick={() => void refetch()} size="sm" type="button" variant="outline">
                {t("errors.retry")}
              </Button>
            </span>
          ) : (
            <code className="font-mono">{data?.version}</code>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
