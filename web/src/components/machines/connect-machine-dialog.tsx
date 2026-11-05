import * as React from "react";
import { Check, Copy, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ToastProvider, ToastViewport, useToast } from "@/components/ui/toast";
import { ApiError } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useConfirmPairing } from "@/lib/queries";

/** "Connect machine" dialog: the title is fixed so the dialog keeps an
 *  accessible name in every step, and the content lives in
 *  `ConnectMachineBody`. Each opening mounts a fresh body (the `key`): the
 *  popup stays mounted during its exit animation, where Escape, Tab and
 *  Enter reopen it inside — so an in-flight submit or copy never reaches
 *  the next opening. The POST is not cancelled; if it confirms the machine
 *  the list behind refreshes the same way (invalidation lives in the
 *  mutation). The toast provider stays mounted on the page, outside the
 *  dialog: on success the dialog closes and the confirmation shows as a
 *  toast outside it, surviving the popup exit. */
export function ConnectMachineDialog() {
  return (
    <ToastProvider>
      <ConnectMachineDialogInner />
      <ToastViewport />
    </ToastProvider>
  );
}

function ConnectMachineDialogInner() {
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
        <ConnectMachineBody close={() => setOpen(false)} key={opening} open={open} />
      </DialogContent>
    </Dialog>
  );
}

/** Step 1: the `nau connect <origin>` command (built from
 *  `window.location.origin`) with copy. Step 2: the XXXX-XXXX code (hyphen
 *  optional) that calls POST /api/pairings/confirm. On success the dialog
 *  closes, focus returns to the trigger and a toast notifies outside it;
 *  errors stay in the open dialog. `open` is the dialog's current state: a
 *  late response only notifies if the opening is still open. */
function ConnectMachineBody({ close, open }: { close: () => void; open: boolean }) {
  const { t } = useI18n();
  const command = `nau connect ${window.location.origin}`;
  const { add } = useToast();

  const [code, setCode] = React.useState("");
  const [copied, setCopied] = React.useState(false);
  const [error, setError] = React.useState("");
  const confirm = useConfirmPairing();

  // Latest `open`: the POST `then` is a promise continuation that does not
  // re-render, so it reads from a ref. A confirmation arriving after the
  // user closed the dialog notifies nothing and does not touch the next
  // opening; the mutation's invalidation still refetches the list.
  const openRef = React.useRef(open);
  React.useEffect(() => {
    openRef.current = open;
  }, [open]);

  // The indicator lasts 2 s and the timer dies with the body.
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
      // Clipboard unavailable (permission, insecure context): the command
      // stays visible to copy manually.
    }
  }

  async function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!code.trim() || confirm.isPending) return;
    setError("");
    // The server normalizes (hyphen optional, any case); send as typed,
    // trimmed of the surrounding spaces.
    await confirm.mutateAsync(code.trim(), {}).then(
      () => {
        if (!openRef.current) return; // opening already closed: late response
        add({ title: t("connect.success") });
        close();
      },
      (err) => {
        if (!openRef.current) return; // late error does not reach the next opening
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
