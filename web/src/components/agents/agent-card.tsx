import { Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { agentSinceMs } from "@/lib/agents";
import type { Agent } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import type { AgentsSearch } from "@/lib/agents-search";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { ReasonPill } from "./reason-pill";

/** Cartão do Board: o cartão inteiro é um link para o detalhe (acessível por
 *  teclado — é um `<a>` nativo). O detalhe recebe os parâmetros da URL de
 *  origem para a barra de volta preservar vista e filtros. */
export function AgentCard({
  agent,
  search,
  className,
}: {
  agent: Agent;
  search: AgentsSearch;
  className?: string;
}) {
  const { locale, t } = useI18n();
  const offline = !agent.machine.online;

  return (
    <Link
      className={cn(
        "flex flex-col gap-1 rounded-lg border bg-card p-3 text-left text-sm outline-none",
        "transition-colors hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring",
        offline && "opacity-60",
        className,
      )}
      params={{ agentId: agent.id }}
      search={search}
      to="/agents/$agentId"
    >
      <span className="truncate text-xs text-muted-foreground">
        {agent.kind} · {agent.model}
      </span>
      <span className="line-clamp-2 font-medium leading-snug">{agent.task ?? agent.name}</span>
      <span className="text-xs text-muted-foreground">{agent.project}</span>
      <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
        <span className="truncate">
          {agent.machine.name} · {agent.terminal}
        </span>
        {offline ? (
          <Badge className="h-4 shrink-0 px-1.5 text-[10px]" variant="secondary">
            {t("machine.offline")}
          </Badge>
        ) : null}
      </span>
      <span className="truncate text-xs text-muted-foreground">{agent.developer.name}</span>
      <span className="mt-0.5 flex min-w-0 items-center justify-between gap-2">
        <span className="shrink-0 text-xs text-muted-foreground">
          {relativeTime(agentSinceMs(agent), locale, undefined, t("time.justNow"))}
        </span>
        <span className="flex min-w-0 flex-wrap items-center justify-end gap-1">
          {agent.state === "needs_you" && agent.reason ? <ReasonPill reason={agent.reason} /> : null}
          {agent.report_ready ? (
            <Badge variant="secondary">{t("report.ready")}</Badge>
          ) : null}
        </span>
      </span>
    </Link>
  );
}
