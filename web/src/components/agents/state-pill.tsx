import { Badge } from "@/components/ui/badge";
import type { AgentState } from "@/lib/api";
import { useI18n, type TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Cor de pílula por estado, a partir dos tokens do sistema.
 *  `needs_you` usa o primário âmbar (o estado que chama o usuário); o
 *  desconhecido usa o destrutivo (algo saiu do padrão). */
const STATE_PILL_CLASSES: Record<AgentState, string> = {
  needs_you: "border-transparent bg-primary/15 text-primary",
  working: "border-transparent bg-secondary text-secondary-foreground",
  done: "border-transparent bg-muted text-muted-foreground",
  idle: "border-border bg-background text-muted-foreground",
  unknown: "border-transparent bg-destructive/15 text-destructive",
};

/** Pílula de estado do agente (Board, Lista, detalhe e histórico). */
export function StatePill({ state, className }: { state: AgentState; className?: string }) {
  const { t } = useI18n();
  return (
    <Badge className={cn(STATE_PILL_CLASSES[state], className)} variant="outline">
      {t(`states.${state}` as TranslationKey)}
    </Badge>
  );
}
