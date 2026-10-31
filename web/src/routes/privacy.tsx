import * as React from "react";
import { createRoute } from "@tanstack/react-router";
import { Eye, EyeOff, ShieldCheck } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { PageError, PageLoading } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import type { AgentsResponse } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useAgents } from "@/lib/queries";
import { RequireSession, useSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: PrivacyPage,
  getParentRoute: () => rootRoute,
  path: "/privacy",
});

/** Prévia: das máquinas do usuário (pelo `developer` dos agentes de
 *  GET /api/agents), quantos agentes chegam em projetos exibidos e quantos
 *  ficam só nas contagens dos ocultos. */
function privacyPreview(agents: AgentsResponse, userId: string) {
  const ownMachineIds = new Set(
    agents.agents.filter((agent) => agent.developer.id === userId).map((agent) => agent.machine.id),
  );
  let shown = 0;
  let hidden = 0;
  for (const agent of agents.agents) {
    if (ownMachineIds.has(agent.machine.id)) shown += 1;
  }
  for (const entry of agents.hidden) {
    if (ownMachineIds.has(entry.machine.id)) {
      const { counts } = entry;
      hidden += counts.done + counts.idle + counts.needs_you + counts.unknown + counts.working;
    }
  }
  return { hidden, shown };
}

/** Privacidade: os três modos de entrega, onde a escolha mora, o que nunca
 *  sai da máquina e a prévia de quanto a máquina do usuário entrega. */
export function PrivacyPage() {
  const { t } = useI18n();
  const session = useSession();
  const { data, error, isPending, refetch } = useAgents();

  const preview = data && session.data ? privacyPreview(data, session.data.id) : null;
  const hasOwnAgents = preview !== null && preview.shown + preview.hidden > 0;

  return (
    <RequireSession>
      <AppShell>
        {isPending ? (
          <PageLoading />
        ) : error && !data ? (
          <PageError onRetry={() => void refetch()} />
        ) : (
          <div className="max-w-3xl space-y-6">
            <section className="space-y-2">
              <h2 className="text-sm font-medium text-muted-foreground">{t("privacy.modes")}</h2>
              <div className="space-y-3">
                <ModeCard icon={Eye} label={t("privacy.mode.show")} text={t("privacy.mode.show.text")} />
                <ModeCard icon={EyeOff} label={t("privacy.mode.hidden")} text={t("privacy.mode.hidden.text")} />
                <ModeCard icon={ShieldCheck} label={t("privacy.mode.never")} text={t("privacy.mode.never.text")} />
              </div>
            </section>

            <section className="space-y-1.5">
              <h2 className="text-sm font-medium text-muted-foreground">{t("privacy.where")}</h2>
              <WhereChoiceText />
            </section>

            <section className="space-y-1.5">
              <h2 className="text-sm font-medium text-muted-foreground">{t("privacy.neverLeaves")}</h2>
              <ul className="grid list-inside list-disc gap-1 text-sm text-muted-foreground sm:grid-cols-2">
                <li>{t("privacy.neverLeaves.brief")}</li>
                <li>{t("privacy.neverLeaves.report")}</li>
                <li>{t("privacy.neverLeaves.screen")}</li>
                <li>{t("privacy.neverLeaves.paths")}</li>
                <li>{t("privacy.neverLeaves.environment")}</li>
                <li>{t("privacy.neverLeaves.commandOutput")}</li>
              </ul>
            </section>

            <section className="space-y-2">
              <h2 className="text-sm font-medium text-muted-foreground">{t("privacy.preview")}</h2>
              <Card size="sm">
                <CardContent>
                  {preview === null ? (
                    <p className="text-sm text-muted-foreground">{t("privacy.preview.empty")}</p>
                  ) : !hasOwnAgents ? (
                    <p className="text-sm text-muted-foreground">{t("privacy.preview.empty")}</p>
                  ) : (
                    <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
                      <span>{t("privacy.preview.shown", { n: preview.shown })}</span>
                      <span>{t("privacy.preview.hidden", { n: preview.hidden })}</span>
                    </div>
                  )}
                </CardContent>
              </Card>
            </section>
          </div>
        )}
      </AppShell>
    </RequireSession>
  );
}

/** Texto de "onde a escolha mora" com o caminho em `<code>`: a frase vem do
 *  dicionário com o placeholder `{path}` e o caminho vai no meio. */
function WhereChoiceText() {
  const { t } = useI18n();
  const placeholder = "\u0000";
  const [beforePath, afterPath] = t("privacy.where.text", { path: placeholder }).split(placeholder);

  return (
    <p className="text-sm text-muted-foreground">
      {beforePath}
      <code className="rounded-sm bg-muted px-1.5 py-0.5 font-mono text-xs">{`.agents/soho.conf`}</code>
      {afterPath}
    </p>
  );
}

function ModeCard({
  icon: Icon,
  label,
  text,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  text: string;
}) {
  return (
    <Card size="sm">
      <CardContent className="flex items-start gap-3">
        <div className="flex size-9 shrink-0 items-center justify-center rounded-lg border bg-muted/40">
          <Icon className="size-4 text-muted-foreground" />
        </div>
        <div className="min-w-0 space-y-1">
          <Badge className="w-fit" variant="secondary">
            {label}
          </Badge>
          <p className="text-sm text-muted-foreground">{text}</p>
        </div>
      </CardContent>
    </Card>
  );
}
