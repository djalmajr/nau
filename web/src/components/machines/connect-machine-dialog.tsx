import * as React from "react";
import { Check, Copy, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useConfirmPairing } from "@/lib/queries";

/** Modal "Conectar máquina": o título é fixo, para o modal ter nome acessível
 *  em todos os passos, e o conteúdo vive em `ConnectMachineBody`. Cada abertura
 *  monta um corpo novo (a `key`): o popup continua montado durante a animação de
 *  saída, e Escape, Tab e Enter reabrem o modal dentro dela. Assim, um envio ou
 *  uma cópia em andamento nunca alcança a abertura seguinte. O POST não é
 *  cancelado; se ele confirmar a máquina, a lista por trás atualiza do mesmo
 *  jeito (a invalidação vive na mutação). */
export function ConnectMachineDialog() {
  const { t } = useI18n();
  const [open, setOpen] = React.useState(false);
  const [opening, setOpening] = React.useState(0);

  return (
    <Dialog
      onOpenChange={(value) => {
        setOpen(value);
        if (value) setOpening((current) => current + 1);
      }}
      open={open}
    >
      <DialogTrigger render={<Button size="sm" />}>
        <Plus className="size-4" />
        {t("machines.connect")}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("connect.title")}</DialogTitle>
        </DialogHeader>
        <ConnectMachineBody key={opening} />
      </DialogContent>
    </Dialog>
  );
}

/** Passo 1: o comando `nau connect <origin>` (montado de
 *  `window.location.origin`) com copiar. Passo 2: o código XXXX-XXXX (com ou
 *  sem hífen), que chama POST /api/pairings/confirm. No sucesso a lista por
 *  trás atualiza sozinha e o botão de sucesso fecha o modal. */
function ConnectMachineBody() {
  const { t } = useI18n();
  const command = `nau connect ${window.location.origin}`;

  const [code, setCode] = React.useState("");
  const [copied, setCopied] = React.useState(false);
  const [error, setError] = React.useState("");
  const [success, setSuccess] = React.useState(false);
  const confirm = useConfirmPairing();

  // O indicador dura 2 s, e o timer morre junto com o corpo.
  React.useEffect(() => {
    if (!copied) return undefined;
    const timer = setTimeout(() => setCopied(false), 2000);
    return () => clearTimeout(timer);
  }, [copied]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
    } catch {
      // clipboard indisponível (permissão, contexto inseguro): o comando
      // continua visível para copiar manualmente.
    }
  }

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!code.trim() || confirm.isPending) return;
    setError("");
    // O servidor normaliza (hífen opcional, qualquer caixa); envia como
    // digitado, apenas sem os espaços nas bordas.
    await confirm.mutateAsync(code.trim(), {}).then(
      () => {
        setSuccess(true);
      },
      (err) => {
        if (err instanceof ApiError) {
          if (err.status === 400) setError(t("connect.invalidCode"));
          else if (err.status === 429) setError(t("connect.rateLimited"));
          else setError(t("errors.generic"));
        } else {
          setError(t("errors.generic"));
        }
      },
    );
  }

  if (success) {
    return (
      <div className="space-y-3" role="status">
        <p className="text-sm font-medium">{t("connect.success")}</p>
        <DialogClose render={<Button size="sm" variant="outline" />}>{t("connect.successLink")}</DialogClose>
      </div>
    );
  }

  return (
    <>
      <section className="space-y-2">
        <h2 className="text-sm font-medium">{t("connect.step1")}</h2>
        <p className="text-sm text-muted-foreground">{t("connect.instruction")}</p>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-sm whitespace-pre-wrap break-all">
            {command}
          </code>
          <Button onClick={() => void copy()} size="icon-sm" type="button" variant="outline">
            {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
            <span className="sr-only">{copied ? t("common.copied") : t("common.copy")}</span>
          </Button>
        </div>
      </section>
      <form className="space-y-3" noValidate onSubmit={(event) => void onSubmit(event)}>
        <h2 className="text-sm font-medium">{t("connect.step2")}</h2>
        <div className="space-y-1.5">
          <Label htmlFor="pairing-code">{t("connect.codeLabel")}</Label>
          <Input
            autoComplete="off"
            id="pairing-code"
            inputMode="text"
            maxLength={9}
            onChange={(event) => setCode(event.target.value)}
            placeholder={t("connect.codePlaceholder")}
            value={code}
          />
        </div>
        {error ? (
          <p className="text-sm text-destructive" role="alert">
            {error}
          </p>
        ) : null}
        <Button disabled={!code.trim() || confirm.isPending} type="submit">
          {t("connect.submit")}
        </Button>
      </form>
    </>
  );
}
