/** Only allow same-origin relative redirects after login. */
export function safeNext(next: string | undefined): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/login')) return '/dashboard'
  return next
}
