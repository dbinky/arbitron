# Phase 3.1 Task 1: Vite + React + TypeScript Project Scaffolding

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the `dashboard/` frontend project with Vite, React, TypeScript, and React Router, with a dark theme shell and dev proxy to the Go API.

**Architecture:** Vite builds to `dashboard/dist/`. During development, Vite dev server proxies `/api` to the Go backend. Production builds produce static assets for Go embedding (Task 2).

**Tech Stack:** Vite 6, React 19, TypeScript, React Router 7

---

### Task 1.1: Initialize Vite project

**Files:**
- Create: `dashboard/package.json`
- Create: `dashboard/tsconfig.json`
- Create: `dashboard/tsconfig.node.json`
- Create: `dashboard/vite.config.ts`
- Create: `dashboard/index.html`
- Create: `dashboard/.gitignore`

- [ ] **Step 1: Scaffold the Vite project**

Run from the repo root:

```bash
cd dashboard && npm create vite@latest . -- --template react-ts
```

If the directory already exists, confirm overwrite.

- [ ] **Step 2: Install dependencies**

```bash
cd dashboard && npm install react-router-dom
```

- [ ] **Step 3: Configure Vite dev proxy**

Replace `dashboard/vite.config.ts`:

```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      '/api/v1/ws': {
        target: 'ws://localhost:8080',
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
```

- [ ] **Step 4: Add dashboard/.gitignore**

```
node_modules/
dist/
```

- [ ] **Step 5: Verify dev server starts**

```bash
cd dashboard && npm run dev
```

Expected: Vite dev server starts on port 5173.

- [ ] **Step 6: Commit**

```bash
git add dashboard/
git commit -m "feat(dashboard): scaffold Vite + React + TypeScript project"
```

---

### Task 1.2: Dark theme and global styles

**Files:**
- Create: `dashboard/src/styles/theme.css`
- Modify: `dashboard/src/main.tsx`
- Delete: `dashboard/src/App.css` (Vite default)
- Delete: `dashboard/src/index.css` (Vite default)

- [ ] **Step 1: Create theme.css with CSS custom properties**

```css
:root {
  --bg-primary: #1a1a2e;
  --bg-secondary: #16162a;
  --bg-surface: #1e1e38;
  --bg-hover: #252548;

  --border-color: #2a2a4a;
  --border-subtle: #222240;

  --text-primary: #e0e0e0;
  --text-secondary: #cccccc;
  --text-muted: #888888;
  --text-dim: #666666;

  --accent: #7c8cf5;
  --accent-hover: #9aa4f7;

  --state-running: #3498db;
  --state-completed: #2ecc71;
  --state-failed: #e74c3c;
  --state-waiting: #e67e22;
  --state-queued: #7f8c8d;
  --state-paused: #9b59b6;

  --priority-high: #f39c12;
  --priority-normal: #888888;
  --priority-low: #666666;

  --danger: #e74c3c;
  --success: #2ecc71;

  --font-family: system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
  --font-mono: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;

  --radius-sm: 4px;
  --radius-md: 6px;
  --radius-lg: 8px;
}

*,
*::before,
*::after {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

html, body, #root {
  height: 100%;
}

body {
  background: var(--bg-primary);
  color: var(--text-primary);
  font-family: var(--font-family);
  font-size: 14px;
  line-height: 1.5;
  -webkit-font-smoothing: antialiased;
}

a {
  color: var(--accent);
  text-decoration: none;
}

a:hover {
  color: var(--accent-hover);
}

::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

::-webkit-scrollbar-track {
  background: var(--bg-primary);
}

::-webkit-scrollbar-thumb {
  background: var(--border-color);
  border-radius: 4px;
}
```

- [ ] **Step 2: Update main.tsx to import theme**

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './styles/theme.css'
import App from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
```

- [ ] **Step 3: Remove Vite default CSS files**

```bash
rm -f dashboard/src/App.css dashboard/src/index.css
```

- [ ] **Step 4: Verify the page renders with dark background**

```bash
cd dashboard && npm run dev
```

Open http://localhost:5173 — should show a dark navy background.

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/styles/ dashboard/src/main.tsx
git rm dashboard/src/App.css dashboard/src/index.css 2>/dev/null; true
git commit -m "feat(dashboard): add dark theme with CSS custom properties"
```

---

### Task 1.3: React Router and App shell

**Files:**
- Modify: `dashboard/src/App.tsx`
- Create: `dashboard/src/routes/QueueView.tsx`
- Create: `dashboard/src/routes/ExecutionView.tsx`
- Create: `dashboard/src/routes/WorkflowsCatalog.tsx`
- Create: `dashboard/src/routes/WorkflowReview.tsx`
- Create: `dashboard/src/routes/Settings.tsx`
- Create: `dashboard/src/routes/NotFound.tsx`

- [ ] **Step 1: Create placeholder route components**

Create `dashboard/src/routes/QueueView.tsx`:
```tsx
export default function QueueView() {
  return <div style={{ padding: 20 }}>Queue View</div>
}
```

Create `dashboard/src/routes/ExecutionView.tsx`:
```tsx
export default function ExecutionView() {
  return <div style={{ padding: 20 }}>Execution View — coming in Phase 2</div>
}
```

Create `dashboard/src/routes/WorkflowsCatalog.tsx`:
```tsx
export default function WorkflowsCatalog() {
  return <div style={{ padding: 20 }}>Workflows Catalog — coming in Phase 3</div>
}
```

Create `dashboard/src/routes/WorkflowReview.tsx`:
```tsx
export default function WorkflowReview() {
  return <div style={{ padding: 20 }}>Workflow Review — coming in Phase 3</div>
}
```

Create `dashboard/src/routes/Settings.tsx`:
```tsx
export default function Settings() {
  return <div style={{ padding: 20 }}>Settings — coming in Phase 3</div>
}
```

Create `dashboard/src/routes/NotFound.tsx`:
```tsx
import { Link } from 'react-router-dom'

export default function NotFound() {
  return (
    <div style={{ padding: 20, textAlign: 'center' }}>
      <h2>404 — Page Not Found</h2>
      <Link to="/">Back to Queue</Link>
    </div>
  )
}
```

- [ ] **Step 2: Set up App.tsx with React Router**

```tsx
import { BrowserRouter, Routes, Route } from 'react-router-dom'
import NavBar from './components/NavBar'
import QueueView from './routes/QueueView'
import ExecutionView from './routes/ExecutionView'
import WorkflowsCatalog from './routes/WorkflowsCatalog'
import WorkflowReview from './routes/WorkflowReview'
import Settings from './routes/Settings'
import NotFound from './routes/NotFound'

export default function App() {
  return (
    <BrowserRouter>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
        <NavBar />
        <main style={{ flex: 1, overflow: 'auto' }}>
          <Routes>
            <Route path="/" element={<QueueView />} />
            <Route path="/instances/:id" element={<ExecutionView />} />
            <Route path="/workflows" element={<WorkflowsCatalog />} />
            <Route path="/workflows/:name" element={<WorkflowReview />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="*" element={<NotFound />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}
```

- [ ] **Step 3: Verify routing works**

Navigate to `/`, `/workflows`, `/settings`, and an unknown route. Each should show its placeholder text.

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/
git commit -m "feat(dashboard): add React Router with route stubs"
```
