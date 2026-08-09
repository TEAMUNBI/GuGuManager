import "@testing-library/jest-dom/vitest";

const storageEntries = new Map<string, string>();
const localStorageMock: Storage = {
  get length() { return storageEntries.size; },
  clear: () => storageEntries.clear(),
  getItem: (key) => storageEntries.get(key) ?? null,
  key: (index) => [...storageEntries.keys()][index] ?? null,
  removeItem: (key) => { storageEntries.delete(key); },
  setItem: (key, value) => { storageEntries.set(key, String(value)); },
};

Object.defineProperty(window, "localStorage", {
  configurable: true,
  value: localStorageMock,
});
