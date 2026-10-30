import { createRoute } from "@tanstack/react-router";
import { Settings2 } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { PageEmpty } from "@/components/page";
import { RequireSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: Page,
});

function Page() {
  return (
    <RequireSession>
      <AppShell>
        <PageEmpty icon={Settings2} />
      </AppShell>
    </RequireSession>
  );
}
