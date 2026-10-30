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
      <SelectTrigger aria-label={label} className="w-40">
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
      <div className="relative">
        <Search
          aria-hidden
          className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
        />
        <Input
          aria-label={t("filters.search")}
          className="h-8 w-56 pl-8"
          onChange={(event) => onChange({ q: event.currentTarget.value })}
          placeholder={t("filters.search")}
          type="search"
          value={value.q}
        />
      </div>
    </div>
  );
}
