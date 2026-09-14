import { Kbd } from '@/components/data/common'
import { Dialog, DialogContent } from '@/components/ui/overlay'

const SHORTCUTS: [string, string[]][] = [
  ['Command palette', ['⌘/Ctrl', 'K']],
  ['Show shortcuts', ['?']],
  ['Toggle sidebar', ['[']],
  ['Go to dashboard / logs / live tail / saved searches / system', ['g', 'd | l | t | s | y']],
  ['Focus query bar', ['/']],
  ['Run query', ['⌘/Ctrl', 'Enter']],
  ['Open time picker', ['t']],
  ['Next / previous row', ['j', 'k']],
  ['Open log detail', ['Enter']],
  ['Filter / exclude focused value', ['f', 'x']],
  ['Close drawer', ['Esc']],
  ['Pause / resume live tail', ['p']],
]

export function ShortcutsHelp({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent title="Keyboard shortcuts">
        <table className="w-full text-base">
          <tbody>
            {SHORTCUTS.map(([label, keys]) => (
              <tr key={label} className="border-b border-border last:border-0">
                <td className="py-1.5 text-muted">{label}</td>
                <td className="py-1.5 text-right whitespace-nowrap">
                  {keys.map((k) => (
                    <span key={k} className="ml-1">
                      <Kbd>{k}</Kbd>
                    </span>
                  ))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </DialogContent>
    </Dialog>
  )
}
