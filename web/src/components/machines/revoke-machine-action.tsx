import * as React from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import type { HubMachine } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useRevokeMachine } from "@/lib/queries";

/** Botão "Revogar chave" com confirmação num diálogo do design system (nunca
 *  o confirm() do navegador). O erro da API (404 quando não é sua) sobe como
 *  mensagem do diálogo; o sucesso invalida a lista (a máquina vira
 *  "revogada"). */
export function RevokeMachineAction({ machine }: { machine: HubMachine }) {
  const { t } = useI18n();
  const [open, setOpen] = React.useState(false);
  const [error, setError] = React.useState("");
  const revoke = useRevokeMachine();

  async function confirm() {
    setError("");
    try {
      await revoke.mutateAsync(machine.id);
      setOpen(false);
    } catch {
      // 404 (máquina não sua) ou falha de rede: o diálogo fica aberto e a
      // mensagem explica.
      setError(t("errors.generic"));
      setOpen(true);
    }
  }

  return (
    <Dialog
      onOpenChange={(value) => {
        setOpen(value);
        if (!value) setError("");
      }}
      open={open}
    >
      <DialogTrigger render={<Button size="sm" variant="outline" />}>
        {t("machines.revoke.confirm")}
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("machines.revoke.title", { name: machine.name })}</DialogTitle>
          <DialogDescription>{t("machines.revoke.description")}</DialogDescription>
          {error ? (
            <p className="text-sm text-destructive" role="alert">
              {error}
            </p>
          ) : null}
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>
            {t("machines.revoke.cancel")}
          </DialogClose>
          <Button disabled={revoke.isPending} onClick={() => void confirm()}>
            {t("machines.revoke.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
