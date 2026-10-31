import { Search } from "lucide-react";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/lib/i18n";
import type { Option } from "@/lib/agents";

/** "Todos" = sem filtro: o valor vazio não vai para a URL. */
const ALL = "";

function FilterSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: Option[];
  onChange: (value: string) => void;
}) {
  const { t } = useI18n();
  const current = options.find((option) => option.id === value);

  return (
    <Select
      onValueChange={(next) => onChange(typeof next === "string" ? next : ALL)}
      value={current ? value : ALL}
    >
      {/* O nome do filtro é visível junto do valor ("Desenvolvedor: Todos");
          o nome acessível continua no aria-label. */}
      <SelectTrigger aria-label={label} className="w-60">
        <span className="shrink-0 text-xs text-muted-foreground">{label}:</span>
        <SelectValue>{current ? current.name : t("filters.all")}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          <SelectItem value={ALL}>{t("filters.all")}</SelectItem>
          {options.map((option) => (
            <SelectItem key={option.id} value={option.id}>
              {option.name}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}

/** Barra de filtros da tela de agentes: desenvolvedor, máquina, projeto e
 *  busca por tarefa ou nome. As opções vêm da lista recebida (mais "Todos");
 *  os valores vivem nos parâmetros de busca da rota. */
export function FilterBar({
  value,
  developers,
  machines,
  projects,
  onChange,
}: {
  value: { developer: string; machine: string; project: string; q: string };
  developers: Option[];
  machines: Option[];
  projects: Option[];
  onChange: (patch: Partial<{ developer: string; machine: string; project: string; q: string }>) => void;
}) {
  const { t } = useI18n();

  return (
    <div className="flex flex-wrap items-center gap-2">
      <FilterSelect
        label={t("filters.developer")}
        onChange={(next) => onChange({ developer: next })}
        options={developers}
        value={value.developer}
      />
      <FilterSelect
        label={t("filters.machine")}
        onChange={(next) => onChange({ machine: next })}
        options={machines}
        value={value.machine}
      />
      <FilterSelect
        label={t("filters.project")}
        onChange={(next) => onChange({ project: next })}
        options={projects}
        value={value.project}
      />
      {/* A busca ocupa a largura disponível (sem sair do viewport em 320/
          375 px) e no máximo 320 px — a mesma escala (80) do w-80 anterior:
          em 1366 continua na linha dos selects e em 800/640/375/320 vai para
          a linha própria, inteira. O placeholder curto ("Tarefa ou agente")
          cabe nos dois idiomas, inclusive na fonte de 16 px do breakpoint
          móvel; o nome acessível continua o rótulo completo (aria-label). */}
      <div className="relative w-80 max-w-full">
        <Search
          aria-hidden
          className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
        />
        <Input
          aria-label={t("filters.search")}
          className="h-8 w-full pl-8"
          onChange={(event) => onChange({ q: event.currentTarget.value })}
          placeholder={t("filters.searchPlaceholder")}
          type="search"
          value={value.q}
        />
      </div>
    </div>
  );
}
