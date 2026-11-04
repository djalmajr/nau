import * as React from "react";
import { createRoute, Link } from "@tanstack/react-router";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";
import { AppShell } from "@/components/app-shell";
import { PageError, PageLoading } from "@/components/page";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart";
import { STATE_ORDER } from "@/lib/agents";
import type { AgentState, OverviewPerMachine, OverviewResponse } from "@/lib/api";
import { useI18n, type Locale, type TranslationKey } from "@/lib/i18n";
import { useOverview } from "@/lib/queries";
import { hourLabel, relativeTime } from "@/lib/time";
import { RequireSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: OverviewPage,
});

/** Visão geral: números do hub, quem espera, agentes por máquina,
 *  trabalho nas últimas 24 h e projetos mais ativos. */
export function OverviewPage() {
  const { data, error, isPending, refetch } = useOverview();

  return (
    <RequireSession>
      <AppShell>
        {isPending ? (
          <PageLoading />
        ) : error || !data ? (
          <PageError onRetry={() => void refetch()} />
        ) : (
          <OverviewContent data={data} />
        )}
      </AppShell>
    </RequireSession>
  );
}

function OverviewContent({ data }: { data: OverviewResponse }) {
  const { locale, t } = useI18n();
  const justNow = t("time.justNow");

  return (
    <div className="space-y-6">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <StatCard
          label={t("overview.needsYou")}
          valueClassName={data.counts.needs_you > 0 ? "text-primary" : undefined}
          value={data.counts.needs_you}
        />
        <StatCard label={t("overview.working")} value={data.counts.working} />
        <StatCard label={t("overview.done")} value={data.counts.done} />
        <StatCard label={t("overview.idle")} value={data.counts.idle} />
        <StatCard
          label={t("overview.machinesOnline")}
          value={t("overview.machinesOnlineValue", {
            online: data.machines.online,
            total: data.machines.online + data.machines.offline,
          })}
        />
        <StatCard label={t("overview.developers")} value={data.developers} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("overview.waiting")}</CardTitle>
        </CardHeader>
        <CardContent>
          {data.needs_you.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("overview.waitingEmpty")}</p>
          ) : (
            <ul className="space-y-1.5">
              {data.needs_you.map((agent) => (
                <li key={agent.id}>
                  <Link
                    className={cn(
                      "flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border p-2.5 text-sm outline-none",
                      "transition-colors hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring",
                    )}
                    params={{ agentId: agent.id }}
                    to="/agents/$agentId"
                  >
                    <span className="min-w-0 flex-1 truncate font-medium">
                      {agent.task ?? agent.name}
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground">{agent.machine.name}</span>
                    <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
                      {relativeTime(agent.since_ms ?? agent.observed_ms, locale, undefined, justNow)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      <section className="space-y-2">
        <h2 className="text-sm font-medium">{t("overview.perMachine")}</h2>
        {data.per_machine.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("overview.perMachineEmpty")}</p>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {data.per_machine.map((entry) => (
              <MachineCard entry={entry} key={entry.machine.id} t={t} />
            ))}
          </div>
        )}
      </section>

      <Working24h hours={data.working_24h} locale={locale} t={t} />

      <Card size="sm">
        <CardHeader>
          <CardTitle>{t("overview.topProjects")}</CardTitle>
        </CardHeader>
        <CardContent>
          {data.top_projects.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("overview.topProjectsEmpty")}</p>
          ) : (
            <ul className="space-y-1.5">
              {data.top_projects.map((project) => (
                <li
                  className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 text-sm"
                  key={project.project}
                >
                  <span className="min-w-0 truncate font-medium">{project.project}</span>
                  <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
                    {project.agents} {t("overview.topProjectsAgents")} · {project.working}{" "}
                    {t("overview.topProjectsWorking")}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** Cartão de número: rótulo pequeno + número em destaque. */
function StatCard({
  label,
  value,
  valueClassName,
}: {
  label: string;
  value: React.ReactNode;
  valueClassName?: string;
}) {
  return (
    <div className="rounded-xl border bg-card p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={cn("mt-1 text-2xl font-semibold tabular-nums", valueClassName)} data-testid="stat-value">
        {value}
      </div>
    </div>
  );
}

/** Ponto de estado nas contagens por máquina (mesmas cores da pílula, sem
 *  disputa de cor: só o ponto, o rótulo usa o texto normal). */
const STATE_DOT_CLASSES: Record<AgentState, string> = {
  needs_you: "bg-primary",
  working: "bg-secondary-foreground",
  done: "bg-muted-foreground",
  idle: "bg-muted-foreground/50",
  unknown: "bg-destructive",
};

function StateDot({ state }: { state: AgentState }) {
  return <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", STATE_DOT_CLASSES[state])} />;
}

/** Cartão por máquina (estilo dos cards de máquina do painel de referência):
 *  nome, ponto online/offline, desenvolvedor e contagens por estado. */
function MachineCard({
  entry,
  t,
}: {
  entry: OverviewPerMachine;
  t: ReturnType<typeof useI18n>["t"];
}) {
  return (
    <Card size="sm">
      <CardContent className="space-y-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            aria-hidden
            className={cn(
              "size-2 shrink-0 rounded-full",
              entry.machine.online ? "bg-primary" : "bg-muted-foreground/50",
            )}
          />
          <span className="min-w-0 truncate font-medium">{entry.machine.name}</span>
          <span className="shrink-0 text-xs text-muted-foreground">
            {t(entry.machine.online ? "machine.online" : "machine.offline")}
          </span>
        </div>
        <div className="truncate text-xs text-muted-foreground">{entry.developer.name}</div>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 border-t pt-2">
          {STATE_ORDER.map((state) => (
            <div className="flex items-center justify-between gap-2 text-xs" key={state}>
              <dt className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
                <StateDot state={state} />
                <span className="truncate">{t(`states.${state}` as TranslationKey)}</span>
              </dt>
              <dd
                className={cn(
                  "tabular-nums",
                  entry.counts[state] === 0 ? "text-muted-foreground/60" : "font-medium",
                )}
              >
                {entry.counts[state]}
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}

/** Trabalho nas últimas 24 h: gráfico de barras real (recharts, pelo
 *  ChartContainer do projeto), uma barra por hora em `var(--primary)`,
 *  lista acessível `sr-only` com o valor de cada hora e a mensagem de
 *  vazio quando nenhum agente está trabalhando. */
function Working24h({
  hours,
  locale,
  t,
}: {
  hours: OverviewResponse["working_24h"];
  locale: Locale;
  t: ReturnType<typeof useI18n>["t"];
}) {
  const max = hours.reduce((best, hour) => Math.max(best, hour.working), 0);
  const data = hours.map((hour) => ({
    label: hourLabel(hour.hour_ms, locale),
    working: hour.working,
  }));
  const config = React.useMemo<ChartConfig>(
    () => ({ working: { label: t("overview.working"), color: "var(--primary)" } }),
    [t],
  );

  return (
    <section className="space-y-2" aria-label={t("overview.working24h")}>
      <h2 className="text-sm font-medium">{t("overview.working24h")}</h2>
      <Card size="sm">
        <CardContent>
          {max === 0 ? (
            <p className="text-sm text-muted-foreground">{t("overview.working24hEmpty")}</p>
          ) : (
            <>
              <ChartContainer className="aspect-auto h-40 w-full" config={config}>
                <BarChart data={data} margin={{ right: 4, top: 8 }}>
                  <CartesianGrid vertical={false} strokeDasharray="2 4" className="stroke-border/60" />
                  <XAxis dataKey="label" tickLine={false} axisLine={{ className: "stroke-border" }} />
                  <YAxis allowDecimals={false} width={24} tickLine={false} axisLine={false} />
                  <ChartTooltip
                    content={
                      <ChartTooltipContent
                        formatter={(value, _name, item) =>
                          t("overview.working24hBar", {
                            count: Number(value),
                            hour: String(item?.payload?.label ?? ""),
                          })
                        }
                        hideLabel
                      />
                    }
                  />
                  <Bar dataKey="working" fill="var(--primary)" />
                </BarChart>
              </ChartContainer>
              <ul className="sr-only">
                {hours.map((hour) => (
                  <li key={hour.hour_ms}>
                    {t("overview.working24hBar", {
                      count: hour.working,
                      hour: hourLabel(hour.hour_ms, locale),
                    })}
                  </li>
                ))}
              </ul>
            </>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
