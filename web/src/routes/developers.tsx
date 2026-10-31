import { createRoute } from "@tanstack/react-router";
import { Users } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { PageEmpty, PageError, PageLoading } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useI18n } from "@/lib/i18n";
import { useDevelopers } from "@/lib/queries";
import { RequireSession, useSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: DevelopersPage,
  getParentRoute: () => rootRoute,
  path: "/developers",
});

/** Desenvolvedores: as contas do hub e o quanto cada uma compartilha com o
 *  usuário (e o usuário com ela). O próprio usuário fica marcado "você". */
export function DevelopersPage() {
  const { t } = useI18n();
  const session = useSession();
  const { data, error, isPending, refetch } = useDevelopers();

  const userId = session.data?.id;
  const developers = data?.developers ?? [];

  return (
    <RequireSession>
      <AppShell>
        {isPending ? (
          <PageLoading />
        ) : error || !data ? (
          <PageError onRetry={() => void refetch()} />
        ) : developers.length === 0 ? (
          <PageEmpty icon={Users} message={t("developers.empty")} />
        ) : (
          <div className="overflow-x-auto rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("machines.developer")}</TableHead>
                  <TableHead>{t("settings.account.email")}</TableHead>
                  <TableHead className="text-right">{t("developers.machines")}</TableHead>
                  <TableHead className="text-right">{t("developers.sharesWithYou")}</TableHead>
                  <TableHead className="text-right">{t("developers.sharedByYou")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {developers.map((developer) => (
                  <TableRow key={developer.id}>
                    <TableCell className="whitespace-nowrap">
                      <span className="font-medium">{developer.name}</span>
                      {developer.id === userId ? (
                        <Badge className="ml-2" variant="secondary">
                          {t("developers.you")}
                        </Badge>
                      ) : null}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-muted-foreground">{developer.email}</TableCell>
                    <TableCell className="text-right tabular-nums">{developer.machines}</TableCell>
                    <TableCell className="text-right tabular-nums">{developer.shares_with_me}</TableCell>
                    <TableCell className="text-right tabular-nums">{developer.shared_by_me}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </AppShell>
    </RequireSession>
  );
}
