import { createRoute, Link } from "@tanstack/react-router";
import { FolderKanban } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { PageEmpty, PageError, PageLoading } from "@/components/page";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { AgentsResponse } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useAgents } from "@/lib/queries";
import { RequireSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: ProjectsPage,
  getParentRoute: () => rootRoute,
  path: "/projects",
});

interface ProjectRow {
  name: string;
  agents: number;
  needsYou: number;
  working: number;
}

/** Projetos exibidos agrupados dos agentes de GET /api/agents (sem endpoint
 *  novo): projeto, total de agentes e os em needs_you e working. */
function projectRows(agents: AgentsResponse["agents"]): ProjectRow[] {
  const byName = new Map<string, ProjectRow>();
  for (const agent of agents) {
    const row = byName.get(agent.project) ?? { agents: 0, needsYou: 0, working: 0, name: agent.project };
    row.agents += 1;
    if (agent.state === "needs_you") row.needsYou += 1;
    if (agent.state === "working") row.working += 1;
    byName.set(agent.project, row);
  }
  return [...byName.values()].sort((a, b) => b.agents - a.agents || a.name.localeCompare(b.name));
}

/** Total de agentes em projetos ocultos, por máquina (soma das cinco
 *  contagens de cada entrada de `hidden`). */
function hiddenByMachine(hidden: AgentsResponse["hidden"]): Array<{ machine: string; total: number }> {
  const byMachine = new Map<string, number>();
  for (const entry of hidden) {
    const { counts } = entry;
    const total = counts.done + counts.idle + counts.needs_you + counts.unknown + counts.working;
    byMachine.set(entry.machine.name, (byMachine.get(entry.machine.name) ?? 0) + total);
  }
  return [...byMachine.entries()]
    .map(([machine, total]) => ({ machine, total }))
    .sort((a, b) => b.total - a.total || a.machine.localeCompare(b.machine));
}

export function ProjectsPage() {
  const { t } = useI18n();
  const { data, error, isPending, refetch } = useAgents();

  const rows = data ? projectRows(data.agents) : [];
  const hidden = data ? hiddenByMachine(data.hidden) : [];

  return (
    <RequireSession>
      <AppShell>
        {isPending ? (
          <PageLoading />
        ) : error || !data ? (
          <PageError onRetry={() => void refetch()} />
        ) : rows.length === 0 ? (
          <PageEmpty icon={FolderKanban} message={t("projects.empty")} />
        ) : (
          <div className="space-y-6">
            <div className="overflow-x-auto rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("projects.column.project")}</TableHead>
                    <TableHead className="text-right">{t("projects.column.agents")}</TableHead>
                    <TableHead className="text-right">{t("states.needs_you")}</TableHead>
                    <TableHead className="text-right">{t("states.working")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((row) => (
                    <TableRow key={row.name}>
                      <TableCell className="whitespace-nowrap">
                        <Link
                          className="text-primary underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                          search={{ project: row.name }}
                          to="/agents"
                        >
                          {row.name}
                        </Link>
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{row.agents}</TableCell>
                      <TableCell className="text-right tabular-nums">{row.needsYou}</TableCell>
                      <TableCell className="text-right tabular-nums">{row.working}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            <section>
              <h2 className="mb-2 text-sm font-medium text-muted-foreground">{t("projects.hiddenTitle")}</h2>
              {hidden.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t("projects.hiddenEmpty")}</p>
              ) : (
                <ul className="space-y-1 text-sm">
                  {hidden.map((entry) => (
                    <li key={entry.machine}>
                      {t("projects.hiddenRow", { machine: entry.machine, n: entry.total })}
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <p className="text-xs text-muted-foreground">{t("projects.note")}</p>
          </div>
        )}
      </AppShell>
    </RequireSession>
  );
}
