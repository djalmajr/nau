// Thin client for the hub session API. Same-origin: in dev, Vite proxies /api
// to the hub (default port 7440); in production the hub serves the built SPA.
const BASE = "/api";
/** Teto de tempo por requisição (JSON, mesma origem). Nenhum endpoint é
 *  streaming/SSE, então abortar é seguro e evita spinner travado num hub lento. */
const REQUEST_TIMEOUT_MS = 20000;

/** Error thrown by the session API client, carrying the HTTP status so the UI
 *  can map it to a human, localized message instead of surfacing raw text. */
export class ApiError extends Error {
  readonly status: number;
  readonly serverError: string;
  constructor(status: number, serverError = "") {
    super(serverError ? `${status}: ${serverError}` : String(status));
    this.name = "ApiError";
    this.status = status;
    this.serverError = serverError;
  }
}

/** true para aborts vindos de AbortSignal.timeout("TimeoutError") ou AbortController ("AbortError"). */
function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && (err.name === "TimeoutError" || err.name === "AbortError");
}

/** true quando o erro é um 401 da API: a sessão expirou/foi revogada. */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401;
}

type UnauthorizedHandler = () => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

/** Tratamento central de 401 de chamadas autenticadas. O app registra um
 *  único handler (ver `registerUnauthorizedRedirect` em lib/session). O 401
 *  do POST de login é excluído em `req`: ali é credencial errada, não sessão
 *  expirada, e a mensagem fica na própria tela. */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(BASE + path, {
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      ...init,
      signal: init?.signal ?? AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (err) {
    // Timeout/abort → ApiError(408) para virar estado de erro/retry na UI;
    // erros de rede genuínos (TypeError) sobem inalterados.
    if (isAbortError(err)) throw new ApiError(408);
    throw err;
  }
  if (!res.ok) {
    let serverError = "";
    try {
      const body = (await res.json()) as { error?: unknown };
      if (typeof body?.error === "string") serverError = body.error;
    } catch {
      // non-JSON error body
    }
    // 401 numa chamada autenticada significa sessão perdida: o handler
    // central remove a sessão do cache e leva o usuário de volta ao login.
    // O POST de login é a exceção (acima).
    const isLoginPost = path === "/session" && String(init?.method ?? "GET").toUpperCase() === "POST";
    if (res.status === 401 && !isLoginPost) unauthorizedHandler?.();
    throw new ApiError(res.status, serverError);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export type SessionRole = "owner" | "member";

export interface SessionUser {
  id: string;
  name: string;
  email: string;
  role: SessionRole;
}

export interface SessionResponse {
  user: SessionUser;
}

export const api = {
  /** GET /api/session — 200 { user } ou 401 { error: "unauthenticated" }. */
  getSession: () => req<SessionResponse>("/session"),
  /** POST /api/session — 200 { user } (+cookie HttpOnly), 401 invalid_credentials, 429 rate_limited. */
  login: (email: string, password: string) =>
    req<SessionResponse>("/session", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    }),
  /** DELETE /api/session — 204. */
  logout: () => req<void>("/session", { method: "DELETE" }),
};
