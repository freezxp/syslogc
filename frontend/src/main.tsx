import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { createAppRouter } from '@/app/router'
import { ApiError, onUnauthorized, setCsrfToken } from '@/api/client'
import { TooltipProvider } from '@/components/ui/overlay'
import { applyTheme, getPrefs } from '@/lib/preferences'

import './styles/index.css'

applyTheme(getPrefs().theme)

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
})

const router = createAppRouter(queryClient)

onUnauthorized(() => {
  const loc = router.state.location
  if (loc.pathname === '/login') return
  setCsrfToken(null)
  queryClient.clear()
  router.navigate({ to: '/login', search: { next: loc.href } })
})

async function start() {
  // Inline env check so production builds tree-shake the mock worker entirely.
  if (import.meta.env.MODE === 'mock') {
    const { startMockWorker } = await import('@/mocks/browser')
    await startMockWorker()
  }
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delayDuration={300}>
          <RouterProvider router={router} />
        </TooltipProvider>
      </QueryClientProvider>
    </StrictMode>,
  )
}

void start()
