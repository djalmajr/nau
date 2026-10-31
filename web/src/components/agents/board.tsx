import { agentsByState, BOARD_COLUMNS, hiddenTotalByState } from "@/lib/agents";
import type { Agent, HiddenCounts } from "@/lib/api";
import type { AgentsSearch } from "@/lib/agents-search";
import { useI18n, type TranslationKey } from "@/lib/i18n";
import { AgentCard } from "./agent-card";
import { HiddenAgentsCard } from "./hidden-card";

/** Board de agentes: quatro colunas (Precisa de você, Trabalhando,
 *  Concluído, Ocioso) com título e contagem; cartões do mais antigo para o
 *  mais novo; cartão de ocultos no fim da coluna; os `unknown` ficam numa
 *  faixa abaixo, quando há algum visível ou oculto (a faixa mostra a
 *  contagem oculta como as colunas). Em telas estreitas as colunas viram
 *  uma pilha vertical. */
export function BoardView({
  agents,
  hidden,
  search,
}: {
  agents: Agent[];
  hidden: HiddenCounts[];
  search: AgentsSearch;
}) {
  const { t } = useI18n();
  const byState = agentsByState(agents);
  const hiddenByState = hiddenTotalByState(hidden);
  const unknown = byState.get("unknown") ?? [];
  const hiddenUnknown = hiddenByState.unknown;

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {BOARD_COLUMNS.map((state) => {
          const list = byState.get(state) ?? [];
          const label = t(`states.${state}` as TranslationKey);
          return (
            <section
              aria-label={label}
              className="flex flex-col gap-2 rounded-xl border bg-muted/20 p-2"
              data-testid="board-column"
              key={state}
            >
              <header className="flex items-center justify-between gap-2 px-1 py-0.5">
                <h3 className="text-sm font-medium">{label}</h3>
                <span className="text-xs tabular-nums text-muted-foreground" data-testid="column-count">
                  {list.length}
                </span>
              </header>
              <div className="flex flex-col gap-2">
                {list.map((agent) => (
                  <AgentCard agent={agent} key={agent.id} search={search} />
                ))}
                {hiddenByState[state] > 0 ? (
                  <HiddenAgentsCard count={hiddenByState[state]} />
                ) : null}
              </div>
            </section>
          );
        })}
      </div>

      {unknown.length > 0 || hiddenUnknown > 0 ? (
        <section aria-label={t("board.unknownTitle")} className="rounded-xl border bg-muted/20 p-2" data-testid="unknown-band">
          <header className="flex items-center justify-between gap-2 px-1 pb-2">
            <h3 className="text-sm font-medium">{t("board.unknownTitle")}</h3>
            <span className="text-xs tabular-nums text-muted-foreground" data-testid="unknown-count">
              {unknown.length}
            </span>
          </header>
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
            {unknown.map((agent) => (
              <AgentCard agent={agent} key={agent.id} search={search} />
            ))}
            {hiddenUnknown > 0 ? <HiddenAgentsCard count={hiddenUnknown} /> : null}
          </div>
        </section>
      ) : null}
    </div>
  );
}
