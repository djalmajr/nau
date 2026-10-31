// Canal ao vivo do quadro (lib/live): o stream por aba, a pílula do status,
// a invalidação das queries do quadro no evento `board` e a recaída por
// consulta GET /api/live quando o stream não abre.
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { userEvent } from "@testing-library/user-event";
import { vi } from "vitest";
import {
  TEST_MEMBER,
  defaultHubState,
  renderHubApp,
  type HubApiState,
} from "@/test/hub-harness";
import { SESSION_QUERY_KEY } from "@/lib/session";
import { liveStatusSnapshot } from "./live-status";

// EventSource falso controlado pelo teste: dispara os eventos que um
// servidor real entregaria (open, error, board) e registra as instâncias.
// `dispatch` continua funcionando depois de close(): simula um evento que
// entrou no buffer antes do close e ainda é entregue (a corrida do evento
// atrasado entre contas).
class FakeEventSource {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;
  static instances: FakeEventSource[] = [];
  static last?: FakeEventSource;
  readonly url: string;
  readyState: number;
  private handlers = new Map<string, Set<EventListener>>();
  constructor(url: string, _options?: EventSourceInit) {
    this.url = url;
    this.readyState = FakeEventSource.CONNECTING;
    FakeEventSource.instances.push(this);
    FakeEventSource.last = this;
  }
  addEventListener(type: string, handler: EventListener) {
    const set = this.handlers.get(type) ?? new Set();
    set.add(handler);
    this.handlers.set(type, set);
  }
  removeEventListener(type: string, handler: EventListener) {
    this.handlers.get(type)?.delete(handler);
  }
  close() {
    this.readyState = FakeEventSource.CLOSED;
  }
  get isClosed() {
    return this.readyState === FakeEventSource.CLOSED;
  }
  dispatch(type: "open" | "error" | "board", data?: string) {
    const event =
      type === "board" ? new MessageEvent(type, { data }) : new Event(type);
    (this.handlers.get(type) ?? new Set<EventListener>()).forEach((handler) =>
      handler(event),
    );
  }
}

function boardData(version: string) {
  return JSON.stringify({ version });
}

// Troca a tarefa do primeiro agente do fixture (o hub "mudou"): o fetch
// falso passa a responder a lista nova nas próximas chamadas.
function withFirstTask(state: HubApiState, task: string): void {
  const agents = state.agents;
  if (!agents) throw new Error("o harness sem agentes não serve o canal");
  state.agents = {
    ...agents,
    agents: agents.agents.map((agent, index) =>
      index === 0 ? { ...agent, task } : agent,
    ),
  };
}

beforeEach(() => {
  FakeEventSource.instances = [];
  FakeEventSource.last = undefined;
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("canal ao vivo do quadro", () => {
  it("abre o stream para a conta logada e a pílula fica \"Ao vivo\"", async () => {
    const state = defaultHubState();
    renderHubApp("/agents", state);
    // A lista de agentes já renderizou (a sessão foi semeada pelo harness).
    await screen.findByText("Retry declined cards in the checkout worker");
    // O canal abriu um EventSource em /api/events.
    const es = FakeEventSource.last;
    expect(es).toBeDefined();
    expect(es?.url).toBe("/api/events");
    // O stream abriu: a pílula do header vira "Ao vivo" (em duas versões
    // de largura: a visível e a sr-only).
    act(() => es?.dispatch("open"));
    expect((await screen.findAllByText("Ao vivo")).length).toBeGreaterThan(0);
    expect(liveStatusSnapshot()).toBe("live");
  });

  it("invalida as queries do quadro quando a versão muda no evento board", async () => {
    const state = defaultHubState();
    renderHubApp("/agents", state);
    await screen.findByText("Retry declined cards in the checkout worker");
    const agentsCalls = () =>
      state.calls.filter((call) => call.url === "/api/agents").length;
    const before = agentsCalls();
    expect(before).toBeGreaterThan(0);

    // O quadro mudou no hub (o servidor responde a nova tarefa) e o stream
    // anuncia a nova versão.
    withFirstTask(state, "Nova tarefa do quadro");
    const es = FakeEventSource.last;
    act(() => es?.dispatch("board", boardData("v1")));

    // A tela atualiza sem recarregar: a nova tarefa renderiza e a query
    // refaz a chamada.
    await screen.findByText("Nova tarefa do quadro");
    await waitFor(() => expect(agentsCalls()).toBeGreaterThan(before));

    // A mesma versão de novo não invalida outra vez.
    const afterSame = agentsCalls();
    act(() => es?.dispatch("board", boardData("v1")));
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(agentsCalls()).toBe(afterSame);
  });

  it("mostra \"Reconectando\" na queda e fecha o stream após três falhas seguidas", async () => {
    renderHubApp("/agents", defaultHubState());
    await screen.findByText("Retry declined cards in the checkout worker");
    const es = FakeEventSource.last;
    act(() => es?.dispatch("open"));
    await screen.findAllByText("Ao vivo");

    // O stream caiu e o EventSource tenta reconectar: a pílula acompanha.
    es!.readyState = FakeEventSource.CONNECTING;
    act(() => es?.dispatch("error"));
    expect((await screen.findAllByText("Reconectando")).length).toBeGreaterThan(0);
    expect(liveStatusSnapshot()).toBe("reconnecting");
    expect(es!.isClosed).toBe(false);

    // Duas falhas seguidas não fecham ainda (a terceira fecha e liga a
    // recaída por consulta).
    act(() => es?.dispatch("error"));
    expect(es!.isClosed).toBe(false);
    act(() => es?.dispatch("error"));
    await screen.findAllByText("Offline");
    expect(liveStatusSnapshot()).toBe("offline");
    expect(es!.isClosed).toBe(true);
    // Nenhuma instância nova (o EventSource não reabriu sozinho).
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it("quando o servidor rejeita o stream, a recaída consulta /api/live e invalida no evento de versão", async () => {
    const state = defaultHubState({ liveVersion: "v1" });
    const { queryClient } = renderHubApp("/agents", state);
    await screen.findByText("Retry declined cards in the checkout worker");
    const es = FakeEventSource.last;

    // O servidor rejeitou o stream (401, 503 ou 429): o EventSource fica
    // CLOSED e não tenta de novo — a pílula é "Offline" e a recaída assume.
    es!.readyState = FakeEventSource.CLOSED;
    act(() => es?.dispatch("error"));
    await screen.findAllByText("Offline");
    await waitFor(() =>
      expect(state.calls.some((call) => call.url === "/api/live")).toBe(true),
    );

    // A leitura da recaída mudou de versão: as queries do quadro invalidam
    // (a nova tarefa chega na tela), como no evento `board`.
    withFirstTask(state, "Tarefa da recaída");
    state.liveVersion = "v2";
    await queryClient.refetchQueries({
      predicate: (query) => query.queryKey[0] === "live",
    });
    await screen.findByText("Tarefa da recaída");
    expect(state.calls.filter((call) => call.url === "/api/agents").length).toBeGreaterThanOrEqual(2);
  });

  it("fecha o stream quando a sessão sai (logout) e ignora evento atrasado", async () => {
    const state = defaultHubState();
    renderHubApp("/agents", state);
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    await screen.findByText("Retry declined cards in the checkout worker");
    const es = FakeEventSource.last;
    act(() => es?.dispatch("open"));
    await screen.findAllByText("Ao vivo");

    // Logout real pelo menu da conta: DELETE no hub, limpa o cache de
    // queries (o que o handler central de 401 também faz) e navega para o
    // login — o canal fecha o stream da conta que saiu. No servidor real o
    // próximo GET /api/session responde 401 (o cookie já foi); o harness
    // replica com sessionFail.
    state.sessionFail = 401;
    await user.click(screen.getByRole("button", { name: "Conta" }));
    await user.click(await screen.findByText("Sair"));
    await screen.findByTestId("login-screen");
    await waitFor(() => expect(es!.isClosed).toBe(true));
    expect(liveStatusSnapshot()).toBe("offline");
    // Sem sessão, nenhum novo stream abre nesta aba.
    expect(FakeEventSource.instances).toHaveLength(1);

    // Um evento `board` que entrou no buffer antes do close ainda é
    // entregue, mas sem sessão não há conta para aplicar: ignorado.
    const agentsCalls = () =>
      state.calls.filter((call) => call.url === "/api/agents").length;
    const before = agentsCalls();
    act(() => es?.dispatch("board", boardData("v-atrasado")));
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(agentsCalls()).toBe(before);
  });

  it("ignora o evento atrasado da conta anterior após trocar de conta", async () => {
    const state = defaultHubState();
    const { queryClient } = renderHubApp("/agents", state);
    await screen.findByText("Retry declined cards in the checkout worker");
    const esU1 = FakeEventSource.last;
    act(() => esU1?.dispatch("open"));
    await screen.findAllByText("Ao vivo");

    // Troca de conta: a sessão vira o membro; o canal fecha o stream da
    // conta anterior e abre um para a nova.
    state.sessionUser = TEST_MEMBER;
    queryClient.setQueryData(SESSION_QUERY_KEY, TEST_MEMBER);
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(2));
    expect(esU1!.isClosed).toBe(true);
    const esU2 = FakeEventSource.last;
    expect(esU2).not.toBe(esU1);

    // O quadro muda no hub e o evento da stream antiga chega atrasado (no
    // buffer antes do close): a sessão atual é outra — o evento é
    // ignorado, sem invalidar o quadro novo nem trazer dado da antiga.
    withFirstTask(state, "Tarefa da conta antiga");
    const agentsCalls = () =>
      state.calls.filter((call) => call.url === "/api/agents").length;
    const before = agentsCalls();
    act(() => esU1?.dispatch("board", boardData("v-antiga")));
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(agentsCalls()).toBe(before);
    expect(screen.queryByText("Tarefa da conta antiga")).not.toBeInTheDocument();

    // A stream da conta nova funciona normalmente para a conta nova.
    act(() => esU2?.dispatch("board", boardData("v-nova")));
    await screen.findByText("Tarefa da conta antiga");
  });
});
