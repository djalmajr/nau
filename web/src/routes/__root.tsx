import { createRootRoute, Outlet } from "@tanstack/react-router";
import { I18nProvider } from "@/lib/i18n";
import { ThemeProvider } from "@/lib/theme";

export const Route = createRootRoute({ component: RootLayout });

function RootLayout() {
  return (
    <I18nProvider>
      <ThemeProvider>
        <Outlet />
      </ThemeProvider>
    </I18nProvider>
  );
}
