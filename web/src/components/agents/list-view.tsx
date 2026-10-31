import { Link, useNavigate } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableFooter,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { agentsInStateOrder, agentSinceMs, hiddenTotal } from "@/lib/agents";
import type { Agent, HiddenCounts } from "@/lib/api";
import type { AgentsSearch } from "@/lib/agents-search";
import { useI18n } from "@/lib/i18n";
import { relativeTime } from "@/lib/time";
import { ReasonPill } from "./reason-pill";
import { StatePill } from "./state-pill";

/** Lista de agentes: a mesma ordem do Board (por estado e depois por
 *  `since_ms`); a linha leva ao detalhe (o nome do agente é o link
 *  acessível; o clique na linha atende o mouse). */
export function ListView({
  agents,
  hidden,
  search,
}: {
  agents: Agent[];
  hidden: HiddenCounts[];
  search: AgentsSearch;
}) {
  const { locale, t } = useI18n();
  const navigate = useNavigate();
  const ordered = agentsInStateOrder(agents);
  const hiddenCount = hiddenTotal(hidden);

  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>{t("agents.column.state")}</TableHead>
          <TableHead>{t("agents.column.agent")}</TableHead>
          <TableHead>{t("agents.column.task")}</TableHead>
          <TableHead>{t("agents.column.project")}</TableHead>
          <TableHead>{t("agents.column.machine")}</TableHead>
          <TableHead>{t("agents.column.developer")}</TableHead>
          <TableHead>{t("agents.column.assistant")}</TableHead>
          <TableHead>{t("agents.column.since")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {ordered.map((agent) => {
          const to = {
            params: { agentId: agent.id },
            search,
            to: "/agents/$agentId",
          } as const;
          return (
            <TableRow
              key={agent.id}
              onClick={() => navigate({ params: { agentId: agent.id }, search, to: "/agents/$agentId" })}
            >
              <TableCell>
                <span className="flex flex-wrap items-center gap-1.5">
                  <StatePill state={agent.state} />
                  {agent.reason ? <ReasonPill reason={agent.reason} /> : null}
                </span>
              </TableCell>
              <TableCell>
                <Link
                  className="font-medium outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                  {...to}
                >
                  {agent.name}
                </Link>
                <span className="block text-xs text-muted-foreground">{agent.role}</span>
              </TableCell>
              <TableCell>
                <span
                  className="block max-w-56 truncate align-middle"
                  title={agent.task ?? undefined}
                >
                  {agent.task ?? "—"}
                </span>
              </TableCell>
              <TableCell>{agent.project}</TableCell>
              <TableCell>
                <span className="flex items-center gap-1.5">
                  {agent.machine.name}
                  {!agent.machine.online ? (
                    <Badge className="h-4 px-1.5 text-[10px]" variant="secondary">
                      {t("machine.offline")}
                    </Badge>
                  ) : null}
                </span>
              </TableCell>
              <TableCell>{agent.developer.name}</TableCell>
              <TableCell>
                <span>{agent.kind}</span>
                <span className="block text-xs text-muted-foreground">{agent.model}</span>
              </TableCell>
              <TableCell className="text-muted-foreground">
                {relativeTime(agentSinceMs(agent), locale, undefined, t("time.justNow"))}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
      {hiddenCount > 0 ? (
        <TableFooter>
          <TableRow>
            <TableCell colSpan={8} className="text-muted-foreground">
              {t("list.hiddenRow", { n: hiddenCount })}
            </TableCell>
          </TableRow>
        </TableFooter>
      ) : null}
    </Table>
  );
}
