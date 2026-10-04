/**
 * Which analyses exist, what unlocks each one, and whether one is worth
 * offering at all.
 *
 * An analysis reads fields only a particular shape of log produces, so it is
 * offered only while some enabled source carries the template that feeds it:
 * a deployment collecting DNS has no use for an Active Directory page it can
 * never fill. The server works out that list (`GET /templates`); this decides
 * what to do with it, and what to say to somebody who arrives at a page their
 * data cannot fill.
 */
import type { SourceTemplate, TemplatesResponse } from '@/api/types'
import type { AnalyticsSearch } from '@/lib/url-state'

/** Analysis ids as the server names them. They name pages, so they are stable. */
export type AnalysisId = 'dns-services' | 'directory'

export interface AnalysisMeta {
  /** The analytics view it is shown as. */
  view: NonNullable<AnalyticsSearch['view']>
  /** Tab label. */
  label: string
  /** What the page answers, said to somebody who cannot have it yet. */
  about: string
  /**
   * Title of the template that feeds it, for when the template list is not
   * readable — a viewer sees the gate message either way.
   */
  template: string
}

export const ANALYSES: Record<AnalysisId, AnalysisMeta> = {
  'dns-services': {
    view: 'trends',
    label: 'Service trends',
    about: 'how many clients reached each online service over time',
    template: 'DNS queries (dnsdist / DNScollector)',
  },
  directory: {
    view: 'directory',
    label: 'Active Directory',
    about: 'who signed in, who could not, whose account locked, and what changed',
    template: 'Active Directory (Windows Security log)',
  },
}

/**
 * Whether an analysis may be offered. `unknown` is kept apart from the other
 * two deliberately: while the list is loading — or when the session may not
 * read it at all — hiding a page would be a guess, and hiding a page somebody
 * has data for is worse than showing one they have to read a sentence about.
 */
export type AnalysisAvailability = 'available' | 'unavailable' | 'unknown'

export function analysisAvailability(id: AnalysisId, templates: TemplatesResponse | undefined): AnalysisAvailability {
  if (!templates) return 'unknown'
  return (templates.analyses ?? []).includes(id) ? 'available' : 'unavailable'
}

/** Only a definite "no" hides a view; anything else leaves it where it was. */
export function analysisShown(availability: AnalysisAvailability): boolean {
  return availability !== 'unavailable'
}

/** The template that feeds an analysis, as the server describes it. */
export function templateForAnalysis(
  id: AnalysisId,
  templates: TemplatesResponse | undefined,
): SourceTemplate | undefined {
  return templates?.templates.find((t) => (t.analyses ?? []).includes(id))
}

/**
 * What to tell somebody who opened an analysis nothing feeds: which template
 * unlocks it, named as they will see it in the source editor.
 */
export function analysisGateMessage(
  id: AnalysisId,
  templates: TemplatesResponse | undefined,
): { title: string; hint: string } {
  const meta = ANALYSES[id]
  const template = templateForAnalysis(id, templates)
  return {
    title: `No source carries ${meta.label} logs yet`,
    hint:
      `This page reads fields only the “${template?.title ?? meta.template}” template produces, and shows ` +
      `${meta.about}. Add a source with that template — or choose it on a source you already have — and this ` +
      `page fills in as its logs arrive.`,
  }
}
