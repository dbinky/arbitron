import { Link, NavLink } from 'react-router-dom'
import NotificationBell from './NotificationBell'
import styles from './NavBar.module.css'

export default function NavBar() {
  return (
    <nav className={styles.navbar}>
      <Link to="/" className={styles.wordmark}>
        Arbitron
      </Link>

      <div className={styles.navLinks}>
        <NavLink
          to="/dashboard"
          className={({ isActive }) =>
            `${styles.navLink} ${isActive ? styles.navLinkActive : ''}`
          }
        >
          Dashboard
        </NavLink>
        <NavLink
          to="/"
          end
          className={({ isActive }) =>
            `${styles.navLink} ${isActive ? styles.navLinkActive : ''}`
          }
        >
          Queue
        </NavLink>
        <NavLink
          to="/workflows"
          className={({ isActive }) =>
            `${styles.navLink} ${isActive ? styles.navLinkActive : ''}`
          }
        >
          Workflows
        </NavLink>
        <NavLink
          to="/settings"
          className={({ isActive }) =>
            `${styles.navLink} ${isActive ? styles.navLinkActive : ''}`
          }
        >
          Settings
        </NavLink>
      </div>

      <div className={styles.spacer} />

      <NotificationBell />
    </nav>
  )
}
