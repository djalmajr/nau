import { useI18n } from "@/lib/i18n";

/** Cartão de projetos ocultos no fim de cada coluna: sem link, só o número
 *  (o hub não expõe os agentes de projetos ocultos). */
export function HiddenAgentsCard({ count, className }: { count: number; className?: string }) {
  const { t } = useI18n();
  return (
    <div
      className={
        "flex items-center justify-center rounded-lg border border-dashed p-3 text-sm text-muted-foreground " +
        (className ?? "")
      }
    >
      {t("board.hiddenCount", { n: count })}
    </div>
  );
}
