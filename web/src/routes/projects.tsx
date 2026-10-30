import { createRoute } from "@tanstack/react-router";
import { FolderKanban } from "lucide-react";
import { AppShell } from "@/components/app-shell";
import { PageEmpty } from "@/components/page";
import { RequireSession } from "@/lib/session";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects",
  component: Page,
});

function Page() {
  return (
    <RequireSession>
      <AppShell>
        <PageEmpty icon={FolderKanban} />
      </AppShell>
    </RequireSession>
  );
}
