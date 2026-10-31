import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "@/lib/api";

// Probes da revisão (achado 1 do review do DJA-628): o emparelhamento manual
// do signal de requisição devolve um disposer, e o `req` o roda no `finally`
// depois da leitura do corpo — nenhum timer pendente após uma requisição
// concluída e nenhum listener no signal do chamador depois do timeout.
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** Rastreia os listeners "abort" registrados no signal do chamador. */
function observe() {
  const caller = new AbortController();
  const listeners = new Set<unknown>();
  const add = caller.signal.addEventListener.bind(caller.signal);
  const remove = caller.signal.removeEventListener.bind(caller.signal);
  vi.spyOn(caller.signal, "addEventListener").mockImplementation((...args) => {
    if (args[0] === "abort") listeners.add(args[1]);
    add(...args);
  });
  vi.spyOn(caller.signal, "removeEventListener").mockImplementation((...args) => {
    if (args[0] === "abort") listeners.delete(args[1]);
    remove(...args);
  });
  return { active: () => listeners.size, caller };
}

it("releases the request timer and caller listener after success", async () => {
  vi.useFakeTimers();
  const observed = observe();
  let combined: AbortSignal | undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_input, init) => {
      combined = init.signal;
      return Response.json({ agents: [], hidden: [], generated_ms: 0 });
    }),
  );
  await api.getAgents({ signal: observed.caller.signal, user: "u1" });
  const pendingTimersAfterCompletion = vi.getTimerCount();
  await vi.advanceTimersByTimeAsync(20_001);
  expect(pendingTimersAfterCompletion).toBe(0);
  expect(observed.active()).toBe(0);
  // O timeout vencendo depois de concluída não aborta a requisição.
  expect(combined?.aborted).toBe(false);
});

it("releases the caller listener when the client timeout wins", async () => {
  vi.useFakeTimers();
  const observed = observe();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      (_input, init) =>
        new Promise((_resolve, reject) =>
          init.signal?.addEventListener("abort", () => reject(init.signal?.reason), { once: true }),
        ),
    ),
  );
  const outcome = api.getAgents({ signal: observed.caller.signal, user: "u1" }).catch((error) => error);
  await vi.advanceTimersByTimeAsync(20_001);
  const error = await outcome;
  expect(error).toBeInstanceOf(ApiError);
  expect((error as ApiError).status).toBe(408);
  expect(observed.active()).toBe(0);
});
