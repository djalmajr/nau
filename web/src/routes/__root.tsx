import { createRootRoute, Outlet } from "@tanstack/react-router";
import { I18nProvider } from "@/lib/i18n";
import { LiveChannel } from "@/lib/live";
import { ThemeProvider } from "@/lib/theme";

export const Route = createRootRoute({ component: RootLayout });

function RootLayout() {
  return (
    <I18nProvider>
      <ThemeProvider>
        {/* Canal ao vivo do quadro (uma vez por aba, acima das rotas). */}
        <LiveChannel />
        <Outlet />
      </ThemeProvider>
    </I18nProvider>
  );
}
