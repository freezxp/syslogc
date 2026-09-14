export const MIN_PASSWORD_LENGTH = 12

export function validateNewPassword(current: string, next: string, confirm: string): string | null {
  if (next.length < MIN_PASSWORD_LENGTH) return `The new password must be at least ${MIN_PASSWORD_LENGTH} characters.`
  if (next.length > 256) return 'The new password must be at most 256 characters.'
  if (next === current) return 'The new password must differ from the current password.'
  if (next !== confirm) return 'The passwords do not match.'
  return null
}
