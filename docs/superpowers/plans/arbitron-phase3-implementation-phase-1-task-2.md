# Phase 3.1 Task 2: NavBar Component

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the persistent top navigation bar with active page highlighting, Arbitron wordmark, and placeholder notification bell.

**Architecture:** NavBar uses React Router's `NavLink` for active state detection. The bell icon renders as an inline SVG. Badge and popover are wired in Phase 4.

**Tech Stack:** React, React Router, CSS Modules

---

### Task 2.1: NavBar component

**Files:**
- Create: `dashboard/src/components/NavBar.tsx`
- Create: `dashboard/src/components/NavBar.module.css`

- [ ] **Step 1: Create NavBar.module.css**

```css
.nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 20px;
  background: var(--bg-secondary);
  border-bottom: 1px solid var(--border-color);
  flex-shrink: 0;
}

.left {
  display: flex;
  align-items: center;
  gap: 24px;
}

.wordmark {
  font-weight: 700;
  font-size: 16px;
  color: var(--accent);
  text-decoration: none;
}

.link {
  color: var(--text-muted);
  font-size: 13px;
  text-decoration: none;
  padding-bottom: 2px;
  border-bottom: 2px solid transparent;
  transition: color 0.15s, border-color 0.15s;
}

.link:hover {
  color: var(--text-secondary);
}

.activeLink {
  color: var(--accent);
  border-bottom-color: var(--accent);
}

.bell {
  position: relative;
  cursor: pointer;
  display: flex;
  align-items: center;
}
```

- [ ] **Step 2: Create NavBar.tsx**

```tsx
import { NavLink, Link } from 'react-router-dom'
import styles from './NavBar.module.css'

function navLinkClass({ isActive }: { isActive: boolean }) {
  return isActive ? `${styles.link} ${styles.activeLink}` : styles.link
}

export default function NavBar() {
  return (
    <nav className={styles.nav}>
      <div className={styles.left}>
        <Link to="/" className={styles.wordmark}>Arbitron</Link>
        <NavLink to="/" end className={navLinkClass}>Queue</NavLink>
        <NavLink to="/workflows" className={navLinkClass}>Workflows</NavLink>
        <NavLink to="/settings" className={navLinkClass}>Settings</NavLink>
      </div>
      <div className={styles.bell}>
        <svg
          width="20"
          height="20"
          viewBox="0 0 24 24"
          fill="none"
          stroke="var(--text-secondary)"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
          <path d="M13.73 21a2 2 0 0 1-3.46 0" />
        </svg>
      </div>
    </nav>
  )
}
```

- [ ] **Step 3: Verify NavBar renders on all pages**

Start the dev server, navigate to `/`, `/workflows`, `/settings`. Verify:
- Arbitron wordmark appears
- Active link is highlighted with accent color and underline
- Bell icon appears on the right
- Clicking links navigates between routes

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/components/NavBar.tsx dashboard/src/components/NavBar.module.css
git commit -m "feat(dashboard): add NavBar component with routing and bell icon"
```
