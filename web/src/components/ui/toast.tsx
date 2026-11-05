"use client";

import { Toast as ToastPrimitive } from "@base-ui/react/toast";
import { X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export function ToastProvider(props: ToastPrimitive.Provider.Props) {
  return <ToastPrimitive.Provider {...props} />;
}

export function useToast() {
  return ToastPrimitive.useToastManager();
}

// Keep notifications mounted outside a dialog's exit lifecycle.
export function ToastViewport({ className, ...props }: ToastPrimitive.Viewport.Props) {
  const { toasts } = useToast();
  const { t } = useI18n();
  return (
    <ToastPrimitive.Portal>
      <ToastPrimitive.Viewport
        aria-label={t("common.notifications")}
        data-slot="toast-viewport"
        className={cn("pointer-events-none fixed top-0 z-50 flex w-full flex-col items-center gap-2 px-4", className)}
        {...props}
      >
        {toasts.map((toast) => (
          <Toast key={toast.id} toast={toast} />
        ))}
      </ToastPrimitive.Viewport>
    </ToastPrimitive.Portal>
  );
}

function Toast({
  toast,
  className,
  ...props
}: ToastPrimitive.Root.Props & { toast: ToastPrimitive.Root.ToastObject }) {
  const { t } = useI18n();
  return (
    <ToastPrimitive.Root
      data-slot="toast"
      toast={toast}
      className={cn(
        "pointer-events-auto flex w-fit max-w-full items-center gap-3 rounded-lg border bg-popover p-3 pr-2 text-sm text-popover-foreground shadow-md",
        className,
      )}
      {...props}
    >
      <ToastPrimitive.Title className="min-w-0 flex-1 font-medium">{toast.title}</ToastPrimitive.Title>
      <ToastPrimitive.Close render={<Button size="icon-xs" variant="ghost" />}>
        <X />
        <span className="sr-only">{t("common.dismiss")}</span>
      </ToastPrimitive.Close>
    </ToastPrimitive.Root>
  );
}
