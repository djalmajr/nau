import * as React from "react";
import { createRoute, Link } from "@tanstack/react-router";
import { ArrowLeft, CircleSlash } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { ReasonPill } from "@/components/agents/reason-pill";
import { StatePill } from "@/components/agents/state-pill";
import { PageError, PageLoading } from "@/components/page";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { isKnownState } from "@/lib/agents";
import { ApiError } from "@/lib/api";
import { parseAgentsSearch, type AgentsSearch } from "@/lib/agents-search";
import { useI18n, type Locale, type TranslationKey } from "@/lib/i18n";
import { useAgent } from "@/lib/queries";
import { absoluteTime, relativeTime } from "@/lib/time";
import { RequireSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/agents/$agentId",
  validateSearch: parseAgentsSearch,
  component: AgentDetailPage,
});

/** Detalhe do agente. Recebe na própria URL os parâmetros de /agents
 *  (`view` e filtros); a barra de volta leva de volta a eles. */
export function AgentDetailPage() {
  const agentId = Route.useParams().agentId;
  const search = Route.useSearch();
  const { t, locale } = useI18n();
  const { data, error, isPending, refetch } = useAgent(agentId);
  const notFound = error instanceof ApiError && error.status === 404;
  const back: { search: AgentsSearch; to: string } = { search, to: "/agents" };

  return (
    <RequireSession>
      <AppShell>
        <div className="space-y-5">
          {isPending ? (
            <>
              <BackBar back={back} t={t} />
              <PageLoading />
            </>
          ) : notFound ? (
            <AgentNotFound back={back} t={t} />
          ) : error || !data ? (
            <>
              <BackBar back={back} t={t} />
              <PageError onRetry={() => void refetch()} />
            </>
          ) : (
            <>
              <BackBar back={back} t={t} />
              <AgentDetail data={data} locale={locale} t={t} />
            </>
          )}
        </div>
      </AppShell>
    </RequireSession>
  );
}

/** Barra de volta "← Agentes" no topo; leva a /agents com os mesmos
 *  parâmetros (view e filtros) que a URL do detalhe carrega. */
function BackBar({
  back,
  t,
}: {
  back: { search: AgentsSearch; to: string };
  t: ReturnType<typeof useI18n>["t"];
}) {
  return (
    <Link
      className={cn(
        "-m-1.5 inline-flex items-center gap-1.5 rounded-md p-1.5 text-sm text-muted-foreground outline-none",
        "transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
      )}
      {...back}
    >
      <ArrowLeft aria-hidden className="size-4" />
      {t("agents.back")}
    </Link>
  );
}

function AgentNotFound({
  back,
  t,
}: {
  back: { search: AgentsSearch; to: string };
  t: ReturnType<typeof useI18n>["t"];
}) {
  return (
    <div className="flex min-h-64 flex-col items-center justify-center gap-4 rounded-lg border border-dashed text-center">
      <div className="flex size-14 items-center justify-center rounded-xl border bg-muted/40">
        <CircleSlash className="size-8 text-muted-foreground" />
      </div>
      <div className="space-y-1">
        <p className="text-sm font-medium">{t("detail.notFound")}</p>
        <p className="text-sm text-muted-foreground">{t("detail.notFoundDescription")}</p>
      </div>
      <Link className={cn(buttonVariants({ variant: "outline" }))} {...back}>
        {t("agents.back")}
      </Link>
    </div>
  );
}

function AgentDetail({
  data,
  locale,
  t,
}: {
  data: NonNullable<ReturnType<typeof useAgent>["data"]>;
  locale: ReturnType<typeof useI18n>["locale"];
  t: ReturnType<typeof useI18n>["t"];
}) {
  const { agent, history } = data;
  const justNow = t("time.justNow");

  return (
    <div className="space-y-5">
      <header className="space-y-1">
        <h2 className="text-lg font-semibold leading-snug">{agent.task ?? agent.name}</h2>
        <p className="text-sm text-muted-foreground">
          {agent.name} · {agent.role}
        </p>
      </header>

      <Card size="sm">
        <CardContent>
          <dl className="grid gap-x-8 gap-y-2.5 sm:grid-cols-2">
            <SummaryRow label={t("detail.state")}>
              <span className="flex flex-wrap items-center justify-end gap-1.5">
                <StatePill state={agent.state} />
                {agent.reason ? <ReasonPill reason={agent.reason} /> : null}
              </span>
            </SummaryRow>
            <SummaryRow label={t("detail.assistant")}>
              {agent.kind} · {agent.model}
            </SummaryRow>
            <SummaryRow label={t("detail.effort")}>{agent.effort}</SummaryRow>
            <SummaryRow label={t("detail.project")}>{agent.project}</SummaryRow>
            <SummaryRow label={t("detail.session")}>{agent.session}</SummaryRow>
            <SummaryRow label={t("detail.terminal")}>{agent.terminal}</SummaryRow>
            <SummaryRow label={t("detail.machine")}>
              <span className="flex items-center gap-1.5">
                <span>{agent.machine.name}</span>
                <Badge
                  className="h-4 px-1.5 text-[10px]"
                  variant={agent.machine.online ? "secondary" : "destructive"}
                >
                  {t(agent.machine.online ? "machine.online" : "machine.offline")}
                </Badge>
              </span>
            </SummaryRow>
            <SummaryRow label={t("detail.developer")}>{agent.developer.name}</SummaryRow>
            <SummaryRow label={t("detail.started")}>
              <TimeValue justNow={justNow} locale={locale} ms={agent.started_ms} />
            </SummaryRow>
            <SummaryRow label={t("detail.since")}>
              <TimeValue justNow={justNow} locale={locale} ms={agent.since_ms} />
            </SummaryRow>
            <SummaryRow label={t("detail.observed")}>
              <TimeValue justNow={justNow} locale={locale} ms={agent.observed_ms} />
            </SummaryRow>
            <SummaryRow label={t("detail.reportReady")}>
              {agent.report_ready ? t("detail.yes") : t("detail.no")}
            </SummaryRow>
          </dl>
        </CardContent>
      </Card>

      <section className="space-y-2" aria-label={t("detail.history")}>
        <h3 className="text-sm font-medium">{t("detail.history")}</h3>
        {history.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("detail.historyEmpty")}</p>
        ) : (
          <ul className="space-y-1.5">
            {history.map((entry, index) => {
              const state = isKnownState(entry.state) ? entry.state : "unknown";
              return (
                <li
                  className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border bg-card p-2.5"
                  key={`${entry.at_ms}-${index}`}
                >
                  <StatePill state={state} />
                  <span className="text-sm">
                    {entry.reason ? t(`reasons.${entry.reason}` as TranslationKey) : t("detail.historyNoReason")}
                  </span>
                  <span className="ml-auto text-xs text-muted-foreground">
                    {relativeTime(entry.at_ms, locale, undefined, justNow)} ·{" "}
                    {absoluteTime(entry.at_ms, locale)}
                  </span>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <p className="rounded-lg border bg-muted/40 p-3 text-xs leading-relaxed text-muted-foreground">
        {t("detail.privacyNote")}
      </p>
    </div>
  );
}

function SummaryRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-sm">{children}</dd>
    </div>
  );
}

/** Tempo relativo + absoluto ("há 3 min · 30/10/2026 15:19"). */
function TimeValue({ ms, locale, justNow }: { ms: number | null; locale: Locale; justNow: string }) {
  if (ms == null) return <>—</>;
  return (
    <span className="whitespace-nowrap">
      {relativeTime(ms, locale, undefined, justNow)}{" "}
      <span className="text-xs text-muted-foreground">{absoluteTime(ms, locale)}</span>
    </span>
  );
}
