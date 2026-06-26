import '@testing-library/jest-dom'

// jsdom does not implement scrollIntoView — mock it globally so component
// auto-scroll effects don't throw in tests.
window.HTMLElement.prototype.scrollIntoView = () => {}

// On Node 25, vitest's jsdom leaves `localStorage` as a non-functional
// null-prototype stub (clear/getItem/setItem are all undefined, because jsdom's
// file-backed storage cannot initialize under the runner's working directory).
// Install a minimal in-memory Storage only when the native one is unusable, so
// hooks that persist view state can be exercised under the real `localStorage`
// contract. Forward-compatible: if a future jsdom/vitest combo ships a working
// storage, this guard is a no-op.
const nativeLs = window.localStorage
if (typeof nativeLs?.getItem !== 'function' || typeof nativeLs?.setItem !== 'function') {
  const store = new Map<string, string>()
  const memoryStorage: Storage = {
    get length() {
      return store.size
    },
    clear: () => store.clear(),
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    removeItem: (k: string) => {
      store.delete(k)
    },
    setItem: (k: string, v: string) => {
      store.set(k, String(v))
    },
  }
  Object.defineProperty(window, 'localStorage', {
    value: memoryStorage,
    configurable: true,
    writable: true,
  })
}
