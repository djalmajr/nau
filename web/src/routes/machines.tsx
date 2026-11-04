import * as React from "react";
import { createRoute } from "@tanstack/react-router";
import { AppShell } from "@/components/app-shell";
import { PageEmpty, PageError, PageLoading } from "@/components/page";
import { MachineCard } from "@/components/machines/machine-card";
import { ConnectMachineDialog } from "@/components/machines/connect-machine-dialog";
import { ServerCog } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { useMachines } from "@/lib/queries";
import { RequireSession, useSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: MachinesPage,
  getParentRoute: () => rootRoute,
  path: "/machines",
});

/** Máquinas: as próprias e as compartilhadas com o usuário. O botão
 *  "Conectar máquina" abre um modal no conteúdo da página (não há mais
 *  ação no cabeçalho global). */
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
      <AppShell>
        {isPending ? (
          <PageLoading />
        ) : error || !data ? (
          <PageError onRetry={() => void refetch()} />
        ) : (
          <div className="space-y-4">
            {/* O botão aparece com a página carregada (inclusive na lista
                vazia) e não em PageLoading nem em PageError. */}
            <div className="flex justify-end">
              <ConnectMachineDialog />
            </div>
            {machines.length === 0 ? (
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
