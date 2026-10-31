import * as React from "react";
import { createRoute, Link } from "@tanstack/react-router";
import { Check, Copy } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { BackBar } from "@/components/page";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useConfirmPairing } from "@/lib/queries";
import { RequireSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: MachinesConnectPage,
  getParentRoute: () => rootRoute,
  path: "/machines/connect",
});

/** Fluxo "Conectar máquina": passo 1 mostra o comando `nau connect <origin>`
 *  (montado de `window.location.origin`) com copiar; passo 2 recebe o código
 *  XXXX-XXXX (com ou sem hífen) e chama POST /api/pairings/confirm. */
export function MachinesConnectPage() {
  const { t } = useI18n();
  const origin = window.location.origin;
  const command = `nau connect ${origin}`;

  const [code, setCode] = React.useState("");
  const [copied, setCopied] = React.useState(false);
  const [error, setError] = React.useState("");
  const [success, setSuccess] = React.useState(false);
  const confirm = useConfirmPairing();

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard indisponível (permissão, contexto inseguro): o comando
      // continua visível para copiar manualmente.
    }
  }

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!code.trim() || confirm.isPending) return;
    setError("");
    // O servidor normaliza (hífen opcional, qualquer caixa); envia como
    // digitado, apenas sem os espaços nas bordas.
    await confirm.mutateAsync(code.trim(), {}).then(
      () => {
        setSuccess(true);
      },
      (err) => {
        if (err instanceof ApiError) {
          if (err.status === 400) setError(t("connect.invalidCode"));
          else if (err.status === 429) setError(t("connect.rateLimited"));
          else setError(t("errors.generic"));
        } else {
          setError(t("errors.generic"));
        }
      },
    );
  }

  return (
    <RequireSession>
      <AppShell>
        <div className="mx-auto max-w-xl space-y-5">
          <BackBar back={{ to: "/machines" }} label={t("connect.back")} />

          {success ? (
            <div className="space-y-3 rounded-lg border p-4" role="status">
              <p className="text-sm font-medium">{t("connect.success")}</p>
              <Link className={cn(buttonVariants({ size: "sm", variant: "outline" }))} to="/machines">
                {t("connect.successLink")}
              </Link>
            </div>
          ) : (
            <div className="space-y-6">
              <section className="space-y-2">
                <h2 className="text-sm font-medium">{t("connect.step1")}</h2>
                <p className="text-sm text-muted-foreground">{t("connect.instruction")}</p>
                <div className="flex items-center gap-2">
                  <code className="min-w-0 flex-1 overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-sm whitespace-pre-wrap break-all">
                    {command}
                  </code>
                  <Button onClick={() => void copy()} size="icon-sm" type="button" variant="outline">
                    {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
                    <span className="sr-only">{copied ? t("common.copied") : t("common.copy")}</span>
                  </Button>
                </div>
              </section>

              <form className="space-y-3" noValidate onSubmit={(event) => void onSubmit(event)}>
                <h2 className="text-sm font-medium">{t("connect.step2")}</h2>
                <div className="space-y-1.5">
                  <Label htmlFor="pairing-code">{t("connect.codeLabel")}</Label>
                  <Input
                    autoComplete="off"
                    id="pairing-code"
                    inputMode="text"
                    maxLength={9}
                    onChange={(event) => setCode(event.target.value)}
                    placeholder={t("connect.codePlaceholder")}
                    value={code}
                  />
                </div>
                {error ? (
                  <p className="text-sm text-destructive" role="alert">
                    {error}
                  </p>
                ) : null}
                <Button disabled={!code.trim() || confirm.isPending} type="submit">
                  {t("connect.submit")}
                </Button>
              </form>
            </div>
          )}
        </div>
      </AppShell>
    </RequireSession>
  );
}
