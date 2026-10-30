import { Link } from "@tanstack/react-router";
import { CircleSlash, TriangleAlert, type LucideIcon } from "lucide-react";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Estado vazio padrão das rotas da casca: ícone e uma frase curta.
 *  `message` sobrescreve a frase padrão quando a tela tem o seu próprio. */
export function PageEmpty({ icon: Icon, message }: { icon: LucideIcon; message?: string }) {
  const { t } = useI18n();

  return (
    <div className="flex min-h-80 flex-col items-center justify-center gap-3 rounded-lg border border-dashed">
      <div className="flex size-14 items-center justify-center rounded-xl border bg-muted/40">
        <Icon className="size-8 text-muted-foreground" />
      </div>
      <p className="text-sm text-muted-foreground">{message ?? t("pages.empty")}</p>
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

/** Estado de erro de uma tela de dados (erro que não é 401): mensagem e
 *  "Tentar de novo" (requisição nova no refetch do TanStack Query). */
export function PageError({ onRetry }: { onRetry: () => void }) {
  const { t } = useI18n();

  return (
    <div className="flex min-h-80 flex-col items-center justify-center gap-4 rounded-lg border border-dashed">
      <div className="flex size-14 items-center justify-center rounded-xl border bg-muted/40">
        <TriangleAlert className="size-8 text-muted-foreground" />
      </div>
      <p className="text-sm text-muted-foreground">{t("errors.generic")}</p>
      <Button onClick={onRetry} variant="outline">
        {t("errors.retry")}
      </Button>
    </div>
  );
}

/** Estado de carregamento padrão das telas de dados. */
export function PageLoading() {
  const { t } = useI18n();

  return (
    <div aria-busy className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Skeleton className="h-8 w-36 rounded-lg" />
        <Skeleton className="h-8 w-36 rounded-lg" />
        <Skeleton className="h-8 w-56 rounded-lg" />
      </div>
      <Skeleton className="h-64 w-full rounded-xl" />
      <span className="sr-only">{t("loading")}</span>
    </div>
  );
}
