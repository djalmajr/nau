import * as React from "react";
import { createRoute } from "@tanstack/react-router";
import { Check, Copy } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { RevokeMachineAction } from "@/components/machines/revoke-machine-action";
import { PageError, PageLoading } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError, type HubMachine, type InviteCreated } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import {
  useCreateInvite,
  useInvites,
  useMachines,
  useRevokeInvite,
  useShareMachine,
  useUnshareMachine,
} from "@/lib/queries";
import { RequireSession, useSession } from "@/lib/session";
import { absoluteTime, relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: AccessPage,
  getParentRoute: () => rootRoute,
  path: "/access",
});

/** Acesso: compartilhamento das máquinas próprias, chaves de máquina,
 *  comandos (em breve) e — só para owner — convites. */
export function AccessPage() {
  const session = useSession();
  const { data, error, isPending, refetch } = useMachines();

  if (isPending) {
    return (
      <RequireSession>
        <AppShell>
          <PageLoading />
        </AppShell>
      </RequireSession>
    );
  }
  if (error || !data) {
    return (
      <RequireSession>
        <AppShell>
          <PageError onRetry={() => void refetch()} />
        </AppShell>
      </RequireSession>
    );
  }

  const isOwner = session.data?.role === "owner";
  return (
    <RequireSession>
      <AppShell>
        <div className="space-y-8">
          <SharingSection machines={data.machines} />
          <MachinesKeysSection machines={data.machines} />
          <CommandsSection />
          {isOwner ? <InvitesSection /> : null}
        </div>
      </AppShell>
    </RequireSession>
  );
}

/** Compartilhamento: para cada máquina própria, com quem ela está
 *  compartilhada (com remover) e o campo de e-mail para compartilhar. */
function SharingSection({ machines }: { machines: NonNullable<ReturnType<typeof useMachines>["data"]>["machines"] }) {
  const { t } = useI18n();
  const session = useSession();
  const own = machines.filter((machine) => machine.developer.id === session.data?.id);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("access.share.title")}</CardTitle>
        <p className="text-sm text-muted-foreground">{t("access.share.description")}</p>
      </CardHeader>
      <CardContent className="space-y-4">
        {own.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("access.share.empty")}</p>
        ) : (
          own.map((machine) => <MachineSharing key={machine.id} machine={machine} />)
        )}
      </CardContent>
    </Card>
  );
}

function MachineSharing({ machine }: { machine: HubMachine }) {
  const { t } = useI18n();
  const [email, setEmail] = React.useState("");
  const [error, setError] = React.useState("");
  /** `developerId` cuja remoção falhou: o compartilhamento continua listado
   *  (o servidor não confirmou) e o item mostra o erro com "Tentar de novo". */
  const [unshareFail, setUnshareFail] = React.useState<string | null>(null);
  const share = useShareMachine();
  const unshare = useUnshareMachine();

  // Máquina revogada: sem lista de compartilhamentos e sem formulário —
  // só o selo "revogada" (a chave já não serve para nada novo).
  if (machine.revoked) {
    return (
      <div className="flex flex-wrap items-center gap-2 border-t pt-4 first:border-t-0 first:pt-0" data-testid={`share-${machine.id}`}>
        <span className="font-medium">{machine.name}</span>
        <Badge variant="destructive">{t("machines.revoked")}</Badge>
      </div>
    );
  }

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!email.trim() || share.isPending) return;
    setError("");
    await share.mutateAsync({ email: email.trim(), id: machine.id }, {}).then(
      () => {
        setEmail("");
      },
      (err) => {
        setError(err instanceof ApiError && err.status === 404 ? t("access.share.unknownEmail") : t("errors.generic"));
      },
    );
  }

  async function handleUnshare(developer: { id: string }) {
    try {
      await unshare.mutateAsync({ id: machine.id, developerId: developer.id });
      if (unshareFail === developer.id) setUnshareFail(null);
    } catch {
      setUnshareFail(developer.id);
    }
  }

  return (
    <div className="space-y-3 border-t pt-4 first:border-t-0 first:pt-0" data-testid={`share-${machine.id}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{machine.name}</span>
        {machine.shared_with.map((developer) => (
          <React.Fragment key={developer.id}>
            <span className="flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs">
              {developer.name}
              <Button
                aria-label={t("access.share.remove") + ` — ${machine.name}`}
                disabled={unshare.isPending}
                onClick={() => void handleUnshare(developer)}
                size="xs"
                type="button"
                variant="ghost"
              >
                {t("access.share.remove")}
              </Button>
            </span>
            {unshareFail === developer.id ? (
              <span className="flex items-center gap-1.5 text-xs">
                <span className="text-destructive" role="alert">
                  {t("errors.generic")}
                </span>
                <Button
                  aria-label={t("errors.retry") + ` — ${developer.name} ${machine.name}`}
                  disabled={unshare.isPending}
                  onClick={() => void handleUnshare(developer)}
                  size="xs"
                  type="button"
                  variant="ghost"
                >
                  {t("errors.retry")}
                </Button>
              </span>
            ) : null}
          </React.Fragment>
        ))}
      </div>
      <form className="flex flex-wrap items-center gap-2" noValidate onSubmit={(event) => void onSubmit(event)}>
        <div className="min-w-40 flex-1">
          <Label className="sr-only" htmlFor={`share-${machine.id}`}>
            {t("access.share.share")} — {machine.name}
          </Label>
          <Input
            id={`share-${machine.id}`}
            onChange={(event) => setEmail(event.target.value)}
            placeholder={t("access.share.emailPlaceholder")}
            type="email"
            value={email}
          />
        </div>
        <Button disabled={!email.trim() || share.isPending} size="sm" type="submit">
          {t("access.share.share")}
        </Button>
      </form>
      {error ? (
        <p className="text-sm text-destructive" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** Chaves de máquina: a mesma lista de /machines; "Revogar" só nas próprias. */
function MachinesKeysSection({ machines }: { machines: NonNullable<ReturnType<typeof useMachines>["data"]>["machines"] }) {
  const { t } = useI18n();
  const session = useSession();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("access.keys.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {machines.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("machines.empty")}</p>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2">
            {machines.map((machine) => (
              <div key={machine.id}>
                <MachineKeyRow machine={machine} own={machine.developer.id === session.data?.id} />
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** Linha de chave: nome, ponto de estado, "revogada" e a ação de revogar
 *  (mesmo diálogo de confirmação da tela Máquinas). */
function MachineKeyRow({ machine, own }: { machine: HubMachine; own: boolean }) {
  const { locale, t } = useI18n();
  const lastSeen =
    machine.last_seen_ms == null
      ? t("machines.neverSeen")
      : t("machines.lastSeen", { time: relativeTime(machine.last_seen_ms, locale, undefined, t("time.justNow")) });

  return (
    <div
      className={cn(
        "flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5",
        machine.revoked && "opacity-60",
      )}
      data-testid={`machine-key-${machine.id}`}
    >
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 truncate font-medium">{machine.name}</span>
          <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", machine.online ? "bg-primary" : "bg-muted-foreground/50")} />
          <span className="shrink-0 text-xs text-muted-foreground">
            {machine.online ? t("machine.online") : t("machine.offline")}
          </span>
          {machine.revoked ? (
            <span className="shrink-0 rounded-md bg-destructive/10 px-1.5 py-0.5 text-xs text-destructive">
              {t("machines.revoked")}
            </span>
          ) : null}
        </div>
        <div className="truncate text-xs text-muted-foreground">
          {machine.developer.name} · {lastSeen}
        </div>
      </div>
      {!machine.revoked && own ? <RevokeMachineAction machine={machine} /> : null}
    </div>
  );
}

/** Comandos nas próprias máquinas: seção esmaecida, sem ação, "Em breve". */
function CommandsSection() {
  const { t } = useI18n();

  return (
    <Card className="opacity-60">
      <CardHeader>
        <CardTitle>{t("access.commands.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <p className="text-sm text-muted-foreground">{t("access.commands.soon")}</p>
      </CardContent>
    </Card>
  );
}

/** Convites (owner): criar por e-mail, link mostrado uma única vez (com
 *  copiar e o aviso de que não aparece de novo) e os pendentes com revogar. */
function InvitesSection() {
  const { locale, t } = useI18n();
  const [email, setEmail] = React.useState("");
  const [error, setError] = React.useState("");
  const [created, setCreated] = React.useState<InviteCreated | null>(null);
  const [copied, setCopied] = React.useState(false);
  const create = useCreateInvite();
  const revoke = useRevokeInvite();
  const { data: invitesData, error: invitesError, refetch: refetchInvites } = useInvites();

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!email.trim() || create.isPending) return;
    setError("");
    await create.mutateAsync(email.trim()).then(
      (invite) => {
        setCreated(invite);
        setEmail("");
      },
      () => {
        // O contrato não define erro de e-mail desconhecido no POST de
        // convite: qualquer falha vira a mensagem genérica.
        setError(t("errors.generic"));
      },
    );
  }

  async function copyLink() {
    if (!created) return;
    const link = inviteLink(created.token);
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard indisponível: o link continua visível para copiar na mão.
    }
  }

  const invites = invitesData?.invites ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("access.invites.title")}</CardTitle>
        <p className="text-sm text-muted-foreground">{t("access.invites.description")}</p>
      </CardHeader>
      <CardContent className="space-y-4">
        {created ? (
          <div className="space-y-2 rounded-lg border p-3" role="status">
            <div className="flex items-baseline justify-between gap-2 text-sm">
              <span className="shrink-0 text-muted-foreground">{t("access.invites.linkLabel")}</span>
              <code className="min-w-0 truncate font-mono">{inviteLink(created.token)}</code>
            </div>
            <div className="flex items-center gap-2">
              <Button onClick={() => void copyLink()} size="icon-sm" type="button" variant="outline">
                {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
                <span className="sr-only">{copied ? t("common.copied") : t("common.copy")}</span>
              </Button>
              <p className="min-w-0 flex-1 text-xs text-muted-foreground">{t("access.invites.shownOnce")}</p>
            </div>
          </div>
        ) : null}

        <form className="flex flex-wrap items-center gap-2" noValidate onSubmit={(event) => void onSubmit(event)}>
          <div className="min-w-40 flex-1">
            <Label className="sr-only" htmlFor="invite-email">
              {t("access.invites.create")}
            </Label>
            <Input
              id="invite-email"
              onChange={(event) => setEmail(event.target.value)}
              placeholder={t("access.share.emailPlaceholder")}
              type="email"
              value={email}
            />
          </div>
          <Button disabled={!email.trim() || create.isPending} size="sm" type="submit">
            {t("access.invites.create")}
          </Button>
        </form>
        {error ? (
          <p className="text-sm text-destructive" role="alert">
            {error}
          </p>
        ) : null}

        <div className="space-y-1.5 border-t pt-4">
          <h3 className="text-sm font-medium text-muted-foreground">{t("access.invites.pending")}</h3>
          {invitesError ? (
            <div className="flex items-center justify-between gap-2 text-sm text-muted-foreground">
              <span>{t("errors.generic")}</span>
              <Button onClick={() => void refetchInvites()} size="sm" type="button" variant="outline">
                {t("errors.retry")}
              </Button>
            </div>
          ) : invites.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("access.invites.pendingEmpty")}</p>
          ) : (
            <ul className="space-y-1.5">
              {invites.map((invite) => (
                <li key={invite.id} className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2">
                  <div className="min-w-0">
                    <div className="truncate text-sm">{invite.email}</div>
                    <div className="text-xs text-muted-foreground" title={absoluteTime(invite.expires_ms, locale)}>
                      {t("access.invites.expires", { time: absoluteTime(invite.expires_ms, locale) })}
                    </div>
                  </div>
                  <Button
                    disabled={revoke.isPending}
                    onClick={() => void revoke.mutateAsync(invite.id)}
                    size="sm"
                    type="button"
                    variant="outline"
                  >
                    {t("access.invites.revoke")}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

/** Link do convite: `<origin>/invite#<token>` — o token vive no fragmento
 *  (nunca na query), para não aparecer no log do servidor nem no histórico. */
function inviteLink(token: string): string {
  return `${window.location.origin}/invite#${token}`;
}
