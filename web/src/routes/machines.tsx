import * as React from "react";
import { createRoute, Link } from "@tanstack/react-router";
import { AppShell } from "@/components/app-shell";
import { PageEmpty, PageError, PageLoading } from "@/components/page";
import { MachineCard } from "@/components/machines/machine-card";
import { buttonVariants } from "@/components/ui/button";
import { ServerCog } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { useMachines } from "@/lib/queries";
import { RequireSession, useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: MachinesPage,
  getParentRoute: () => rootRoute,
  path: "/machines",
});

/** Máquinas: as próprias e as compartilhadas com o usuário. O botão do
 *  cabeçalho leva ao fluxo "Conectar máquina". */
export function MachinesPage() {
  const { t } = useI18n();
  const session = useSession();
  const { data, error, isPending, refetch } = useMachines();

  const userId = session.data?.id;
  const machines = data?.machines ?? [];
  const own = machines.filter((machine) => machine.developer.id === userId);
  const shared = machines.filter((machine) => machine.developer.id !== userId);

  return (
    <RequireSession>
      <AppShell
        headerActions={
          <Link className={cn(buttonVariants({ size: "sm" }))} to="/machines/connect">
            {t("machines.connect")}
          </Link>
        }
      >
      {isPending ? (
        <PageLoading />
      ) : error || !data ? (
        <PageError onRetry={() => void refetch()} />
      ) : machines.length === 0 ? (
        <PageEmpty icon={ServerCog} message={t("machines.empty")} />
      ) : (
        <div className="space-y-6">
          {own.length > 0 ? (
            <MachinesSection title={t("machines.ownSection")}>
              {own.map((machine) => (
                <MachineCard key={machine.id} machine={machine} own />
              ))}
            </MachinesSection>
          ) : null}
          {shared.length > 0 ? (
            <MachinesSection title={t("machines.sharedSection")}>
              {shared.map((machine) => (
                <MachineCard key={machine.id} machine={machine} own={false} />
              ))}
            </MachinesSection>
          ) : null}
        </div>
      )}
      </AppShell>
    </RequireSession>
  );
}

function MachinesSection({ title, children }: { children: React.ReactNode; title: string }) {
  return (
    <section>
      <h2 className="mb-3 text-sm font-medium text-muted-foreground">{title}</h2>
      <div className="grid gap-4 sm:grid-cols-2">{children}</div>
    </section>
  );
}
