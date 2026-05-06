import { Link } from 'react-router-dom'

export default function NotFound() {
  return (
    <div style={{ padding: 40, textAlign: 'center' }}>
      <h1 style={{ fontSize: 48, marginBottom: 16, color: 'var(--text-muted)' }}>404</h1>
      <p style={{ marginBottom: 24, color: 'var(--text-secondary)' }}>Page not found</p>
      <Link to="/">Back to Queue</Link>
    </div>
  )
}
