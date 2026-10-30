import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { TooltipProvider } from "@/components/ui/tooltip";
import { registerUnauthorizedRedirect } from "@/lib/session";
import { router } from "./router";
import "./index.css";

const el = document.getElementById("root");
if (!el) throw new Error("root element not found");

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Refresh suave: mantém os últimos dados enquanto revalida.
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      refetchIntervalInBackground: false,
      retry: 1,
    },
  },
});

// 401 em qualquer chamada (inclusive o refetch da sessão) remove a sessão do
// cache e leva o usuário para o login; o POST de login é a exceção (a
// mensagem fica na tela).
registerUnauthorizedRedirect(queryClient, router);

createRoot(el).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      {/* delay curto: tooltip responde rápido sem travar o usuário */}
      <TooltipProvider delay={200}>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
);
