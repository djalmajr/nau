import { createRoute } from "@tanstack/react-router";
import { InviteScreen } from "@/components/invite";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  component: InviteScreen,
  getParentRoute: () => rootRoute,
  path: "/invite",
});
