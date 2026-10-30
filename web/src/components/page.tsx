import { Link } from "@tanstack/react-router";
import { CircleSlash, type LucideIcon } from "lucide-react";
import { buttonVariants } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Estado vazio padrão das rotas da casca: ícone e uma frase curta.
 *  As telas de conteúdo chegam em cards seguintes. */
export function PageEmpty({ icon: Icon }: { icon: LucideIcon }) {
  const { t } = useI18n();

  return (
    <div className="flex min-h-80 flex-col items-center justify-center gap-3 rounded-lg border border-dashed">
      <div className="flex size-14 items-center justify-center rounded-xl border bg-muted/40">
        <Icon className="size-8 text-muted-foreground" />
      </div>
      <p className="text-sm text-muted-foreground">{t("pages.empty")}</p>
    </div>
  );
}

/** Rota desconhecida: página "não encontrado" dentro da casca. */
export function NotFoundPage() {
  const { t } = useI18n();

  return (
    <div className="flex min-h-80 flex-col items-center justify-center gap-4 rounded-lg border border-dashed">
      <div className="flex size-14 items-center justify-center rounded-xl border bg-muted/40">
        <CircleSlash className="size-8 text-muted-foreground" />
      </div>
      <p className="text-sm text-muted-foreground">{t("notFound.description")}</p>
      <Link className={cn(buttonVariants({ variant: "outline" }))} to="/">
        {t("notFound.back")}
      </Link>
    </div>
  );
}
