import { createRoute } from "@tanstack/react-router";
import { AppShell } from "@/components/app-shell";
import { NotFoundPage } from "@/components/page";
import { RequireSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/{$}",
  component: Page,
});

function Page() {
  return (
    <RequireSession>
      <AppShell>
        <NotFoundPage />
      </AppShell>
    </RequireSession>
  );
}
