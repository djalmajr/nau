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
  return {
    addEventListener: () => {},
    addListener: () => {},
    dispatchEvent: () => false,
    matches: false,
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
