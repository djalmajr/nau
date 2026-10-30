import { LayoutGrid, List } from "lucide-react";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useI18n } from "@/lib/i18n";
import type { AgentsView } from "@/lib/agents-search";

/** Troca Board | Lista como controle segmentado (abas acessíveis). O estado
 *  vive na URL; a UI só reporta a escolha. */
export function ViewToggle({
  value,
  onChange,
}: {
  value: AgentsView;
  onChange: (view: AgentsView) => void;
}) {
  const { t } = useI18n();

  return (
    <Tabs
      onValueChange={(next) => onChange(next as AgentsView)}
      value={value}
    >
      <TabsList variant="segmented">
        <TabsTrigger value="board">
          <LayoutGrid />
          {t("agents.view.board")}
        </TabsTrigger>
        <TabsTrigger value="list">
          <List />
          {t("agents.view.list")}
        </TabsTrigger>
      </TabsList>
    </Tabs>
  );
}
