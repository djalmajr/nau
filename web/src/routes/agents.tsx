import * as React from "react";
import { createRoute, useNavigate } from "@tanstack/react-router";
import { Bot } from "lucide-react";
import { ViewToggle } from "@/components/agents/view-toggle";
import { AppShell } from "@/components/app-shell";
import { BoardView } from "@/components/agents/board";
import { FilterBar } from "@/components/agents/filter-bar";
import { ListView } from "@/components/agents/list-view";
import { PageEmpty, PageError, PageLoading } from "@/components/page";
import { filterAgents, hiddenTotal, type AgentFilters } from "@/lib/agents";
import { filterOptions, effectiveView, parseAgentsSearch, type AgentsSearch } from "@/lib/agents-search";
import type { AgentsResponse } from "@/lib/api";
import { useAgents } from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { RequireSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/agents",
  validateSearch: parseAgentsSearch,
  component: AgentsPage,
});

/** Tela de agentes: Board | Lista na mesma rota; a troca e os filtros vivem
 *  nos parâmetros de busca (`view`, `developer`, `machine`, `project`, `q`).
 *  O detalhe recebe os mesmos parâmetros para a barra de volta preservá-los. */
export function AgentsPage() {
  const search = Route.useSearch();
  const navigate = useNavigate();
  const { t } = useI18n();
  const { data, error, isPending, refetch } = useAgents();

  const filters: AgentFilters = React.useMemo(
    () => ({
      developer: search.developer ?? "",
      machine: search.machine ?? "",
      project: search.project ?? "",
      q: search.q ?? "",
    }),
    [search.developer, search.machine, search.project, search.q],
  );
  const hasFilters = Boolean(filters.developer || filters.machine || filters.project || filters.q);
  const view = effectiveView(search);

  /** Atualiza a URL (replace: filtro é estado transitório, não histórico). */
  const update = React.useCallback(
    (patch: { view?: AgentsSearch["view"] } & Partial<AgentFilters>) => {
      const next: AgentsSearch = { view: patch.view ?? search.view };
      const merged: AgentFilters = { ...filters, ...patch };
      if (merged.developer) next.developer = merged.developer;
      if (merged.machine) next.machine = merged.machine;
      if (merged.project) next.project = merged.project;
      if (merged.q) next.q = merged.q;
      navigate({ replace: true, search: next, to: "/agents" });
    },
    [filters, navigate, search.view],
  );

  // Opções dos filtros vêm da lista recebida; um valor que não existe mais
  // (máquina fora da resposta, por exemplo) volta ao padrão — sem filtro.
  const options = React.useMemo(() => (data ? filterOptions(data) : null), [data]);
  React.useEffect(() => {
    if (!options) return;
    const invalid: Array<"developer" | "machine" | "project"> = [];
    if (search.developer && !options.developers.some((option) => option.id === search.developer)) {
      invalid.push("developer");
    }
    if (search.machine && !options.machines.some((option) => option.id === search.machine)) {
      invalid.push("machine");
    }
    if (search.project && !options.projects.some((option) => option.id === search.project)) {
      invalid.push("project");
    }
    if (invalid.length === 0) return;
    const next: AgentsSearch = { view: search.view };
    if (!invalid.includes("developer")) next.developer = search.developer;
    if (!invalid.includes("machine")) next.machine = search.machine;
    if (!invalid.includes("project")) next.project = search.project;
    if (search.q) next.q = search.q;
    navigate({ replace: true, search: next, to: "/agents" });
  }, [navigate, options, search]);

  return (
    <RequireSession>
      <AppShell
        headerActions={<ViewToggle onChange={(nextView) => update({ view: nextView })} value={view} />}
      >
        {isPending ? (
          <PageLoading />
        ) : error || !data ? (
          <PageError onRetry={() => void refetch()} />
        ) : (
          <AgentsContent
            data={data}
            filters={filters}
            hasFilters={hasFilters}
            onChange={update}
            t={t}
            search={search}
          />
        )}
      </AppShell>
    </RequireSession>
  );
}

function AgentsContent({
  data,
  filters,
  hasFilters,
  onChange,
  t,
  search,
}: {
  data: AgentsResponse;
  filters: AgentFilters;
  hasFilters: boolean;
  onChange: (patch: Partial<AgentFilters>) => void;
  t: ReturnType<typeof useI18n>["t"];
  search: AgentsSearch;
}) {
  const options = React.useMemo(() => filterOptions(data), [data]);
  const visible = filterAgents(data.agents, filters);
  // O filtro de máquina também corta os ocultos (a única informação que eles
  // expõem); os demais filtros não alcançam projetos ocultos.
  const hidden = search.machine
    ? data.hidden.filter((entry) => entry.machine.id === search.machine)
    : data.hidden;
  const hasAny = visible.length > 0 || hiddenTotal(hidden) > 0;

  return (
    <div className="space-y-4">
      <FilterBar
        developers={options.developers}
        machines={options.machines}
        onChange={onChange}
        projects={options.projects}
        value={filters}
      />
      {!hasAny ? (
        <PageEmpty
          icon={Bot}
          message={hasFilters ? t("agents.emptyFiltered") : t("agents.empty")}
        />
      ) : effectiveView(search) === "board" ? (
        <BoardView agents={visible} hidden={hidden} search={search} />
      ) : (
        <ListView agents={visible} hidden={hidden} search={search} />
      )}
    </div>
  );
}
