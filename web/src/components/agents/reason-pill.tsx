import { Badge } from "@/components/ui/badge";
import type { AgentReason } from "@/lib/api";
import { useI18n, type TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Pílula do motivo (pergunta, aprovação, quota…); o texto vem do dicionário
 *  e a cor fica neutra para não disputar com a pílula de estado. */
export function ReasonPill({ reason, className }: { reason: AgentReason; className?: string }) {
  const { t } = useI18n();
  return (
    <Badge className={cn("text-muted-foreground", className)} variant="outline">
      {t(`reasons.${reason}` as TranslationKey)}
    </Badge>
  );
}
