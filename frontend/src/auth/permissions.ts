import { useSession } from '@/api/hooks'
import type { Permission } from '@/api/types'

export function useCan(): (p: Permission) => boolean {
  const { data } = useSession()
  const perms = new Set(data?.permissions ?? [])
  return (p) => perms.has(p)
}
