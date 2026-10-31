import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import type { HubMachine } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { RevokeMachineAction } from "./revoke-machine-action";

/** Ponto online/offline + rótulo, no padrão da pílula do header (token do
 *  design system, sem cor fora de variável). */
function OnlineDot({ online, label }: { online: boolean; label: string }) {
  return (
    <Badge className="shrink-0" variant="outline">
      <span aria-hidden className={cn("size-1.5 rounded-full", online ? "bg-primary" : "bg-muted-foreground/50")} />
      {label}
    </Badge>
  );
}

/** Cartão de máquina (estilo machine-cards do apigate: card por máquina com
 *  nome + selo de estado e linhas de fato). Revogada aparece esmaecida com o
 *  selo "revogada". `own` liga a ação de revogar (só nas próprias). */
export function MachineCard({ machine, own }: { machine: HubMachine; own: boolean }) {
  const { locale, t } = useI18n();
  const lastSeen =
    machine.last_seen_ms == null
      ? t("machines.neverSeen")
      : t("machines.lastSeen", { time: relativeTime(machine.last_seen_ms, locale, undefined, t("time.justNow")) });

  return (
    <Card size="sm" className={cn(machine.revoked && "opacity-60")} data-testid={`machine-card-${machine.id}`}>
      <CardContent className="space-y-3">
        <div className="flex items-start justify-between gap-3">
          <div className="flex min-w-0 items-center gap-2">
            <span className="min-w-0 truncate font-medium">{machine.name}</span>
            <OnlineDot online={machine.online} label={t(machine.online ? "machine.online" : "machine.offline")} />
            {machine.revoked ? (
              <Badge className="shrink-0" variant="destructive">
                {t("machines.revoked")}
              </Badge>
            ) : null}
          </div>
          {!machine.revoked && own ? <RevokeMachineAction machine={machine} /> : null}
        </div>

        <div className="space-y-1.5 border-t pt-3 text-sm">
          <div className="flex items-baseline justify-between gap-2">
            <span className="shrink-0 text-xs text-muted-foreground">{t("machines.developer")}</span>
            <span className="min-w-0 truncate">{machine.developer.name}</span>
          </div>
          <div className="text-xs text-muted-foreground">{lastSeen}</div>
          {own ? (
            <div className="flex items-baseline justify-between gap-2">
              <span className="shrink-0 text-xs text-muted-foreground">{t("machines.sharedWith")}</span>
              <span className="min-w-0 truncate text-right">
                {machine.shared_with.length > 0 ? machine.shared_with.map((developer) => developer.name).join(", ") : "—"}
              </span>
            </div>
          ) : (
            <div>
              <Badge className="w-fit" variant="secondary">
                {t("machines.sharedWithYou")}
              </Badge>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
