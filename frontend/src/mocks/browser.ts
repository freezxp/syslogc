import { setupWorker } from 'msw/browser'

import { handlers, setMockAssistant } from './handlers'

export async function startMockWorker(): Promise<void> {
  const worker = setupWorker(...handlers)
  await worker.start({ onUnhandledRequest: 'bypass', quiet: true })
  // The assistant has three states and only one of them can be the default.
  // There is no way to reach the other two from the interface — that is the
  // point of them — so the mock build exposes the switch, which lets the
  // "it is off, show nothing" and "it broke" paths actually be looked at.
  // Mock builds only: `main.tsx` never imports this module otherwise.
  ;(window as unknown as { syslogcMockAssistant: typeof setMockAssistant }).syslogcMockAssistant = setMockAssistant
}
