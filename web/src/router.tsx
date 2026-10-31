import { createRouter } from "@tanstack/react-router";
import { Route as rootRoute } from "./routes/__root";
import { Route as indexRoute } from "./routes/index";
import { Route as agentsRoute } from "./routes/agents";
import { Route as agentDetailRoute } from "./routes/agents-detail";
import { Route as machinesRoute } from "./routes/machines";
import { Route as developersRoute } from "./routes/developers";
import { Route as projectsRoute } from "./routes/projects";
import { Route as privacyRoute } from "./routes/privacy";
import { Route as accessRoute } from "./routes/access";
import { Route as settingsRoute } from "./routes/settings";
import { Route as loginRoute } from "./routes/login";
import { Route as notFoundRoute } from "./routes/not-found";

const routeTree = rootRoute.addChildren([
  indexRoute,
  agentsRoute,
  agentDetailRoute,
  machinesRoute,
  developersRoute,
  projectsRoute,
  privacyRoute,
  accessRoute,
  settingsRoute,
  loginRoute,
  notFoundRoute,
]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
