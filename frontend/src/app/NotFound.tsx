import { Link } from '@tanstack/react-router'

export function NotFound() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
      <p className="text-2xl font-semibold">Page not found</p>
      <p className="text-muted">The page you are looking for does not exist.</p>
      <Link to="/dashboard" className="text-accent hover:underline">
        Go to the dashboard
      </Link>
    </div>
  )
}
