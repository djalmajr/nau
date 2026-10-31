import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

function createMemoryStorage(): Storage {
  const items = new Map<string, string>();
  return {
    get length() {
      return items.size;
    },
    clear() {
      items.clear();
    },
    getItem(key: string) {
      return items.get(key) ?? null;
    },
    key(index: number) {
      return Array.from(items.keys())[index] ?? null;
    },
    removeItem(key: string) {
      items.delete(key);
    },
    setItem(key: string, value: string) {
      items.set(key, value);
    },
  };
}

function defineStorage(name: "localStorage" | "sessionStorage") {
  const storage = createMemoryStorage();
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value: storage,
  });
  Object.defineProperty(window, name, {
    configurable: true,
    value: storage,
  });
}

defineStorage("localStorage");
defineStorage("sessionStorage");

// jsdom não implementa matchMedia; o ThemeProvider (tema "system") e a
// sidebar consultam prefers-color-scheme / max-width. Função real (não vi.fn)
// para sobreviver a vi.restoreAllMocks().
function mediaQueryList(query: string) {
  // Emula o viewport padrão do jsdom (1024 px): `min-width: 640px` casa
  // (janela larga — o header guarda a ação da página na linha do título);
  // `max-width: <767px>` e `prefers-color-scheme: dark` não casam.
  const matches = /min-width:\s*640px/.test(query);
  return {
    addEventListener: () => {},
    addListener: () => {},
    dispatchEvent: () => false,
    matches,
    media: query,
    onchange: null,
    removeEventListener: () => {},
    removeListener: () => {},
  };
}
Object.defineProperty(window, "matchMedia", {
  configurable: true,
  value: (query: string) => mediaQueryList(query),
});

// jsdom não implementa ResizeObserver, scrollIntoView nem getAnimations
// (usados pelo base-ui).
class ResizeObserverStub {
  disconnect() {}
  observe() {}
  unobserve() {}
}
Object.defineProperty(globalThis, "ResizeObserver", {
  configurable: true,
  value: ResizeObserverStub,
});
Element.prototype.scrollIntoView = () => {};
Element.prototype.getAnimations = () => [];

afterEach(() => cleanup());
