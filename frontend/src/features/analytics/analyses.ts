/**
 * Which analyses exist, what unlocks each one, and whether one is worth
 * offering at all.
 *
 * An analysis reads fields only a particular shape of log produces, so it is
 * offered only while some enabled source carries the template — and, where the
 * template is built from parts, the part — that feeds it: a deployment
 * collecting DNS has no use for an Active Directory page it can never fill,
 * and a Windows source carrying only IIS has no use for a SQL Server one. The
 * server works out that list (`GET /templates`); this decides what to do with
 * it, and what to say to somebody who arrives at a page their data cannot fill.
 */
import type { SourceTemplate, TemplatePart, TemplatesResponse } from '@/api/types'
import type { AnalyticsSearch } from '@/lib/url-state'

/** Analysis ids as the server names them. They name pages, so they are stable. */
export type AnalysisId = 'dns-services' | 'directory' | 'mssql' | 'iis'

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
  /**
   * Title of the part of that template which feeds it, where the template
   * carries several kinds of log. Same purpose as `template`: a fallback for
   * when the list cannot be read, so the message still says what to switch on.
   */
  part?: string
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
    template: 'Microsoft Windows Server',
    part: 'Active Directory',
  },
  mssql: {
    view: 'mssql',
    label: 'SQL Server',
    about: 'which accounts failed to sign in and from where, plus deadlocks, backups and the errors before an outage',
    template: 'Microsoft Windows Server',
    part: 'SQL Server',
  },
  iis: {
    view: 'iis',
    label: 'IIS',
    about: 'which requests the web server answered, what it answered with, and which URLs are slow',
    template: 'Microsoft Windows Server',
    part: 'IIS (web requests)',
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

/** Where an analysis comes from: a template, and the part of it that feeds it. */
export interface AnalysisSource {
  template: SourceTemplate
  /** Absent when the template feeds the analysis on its own. */
  part?: TemplatePart
}

/**
 * The template that feeds an analysis, and the part of it that does, as the
 * server describes them. A template built from parts leaves its own `analyses`
 * empty, so the parts have to be looked through too — otherwise the Windows
 * analyses would have nothing to name.
 */
export function analysisSource(id: AnalysisId, templates: TemplatesResponse | undefined): AnalysisSource | undefined {
  for (const template of templates?.templates ?? []) {
    if ((template.analyses ?? []).includes(id)) return { template }
    const part = (template.parts ?? []).find((p) => (p.analyses ?? []).includes(id))
    if (part) return { template, part }
  }
  return undefined
}

/** The template that feeds an analysis, as the server describes it. */
export function templateForAnalysis(
  id: AnalysisId,
  templates: TemplatesResponse | undefined,
): SourceTemplate | undefined {
  return analysisSource(id, templates)?.template
}

/**
 * What to tell somebody who opened an analysis nothing feeds: which template
 * unlocks it, and which of its parts, named as they will see them in the
 * source editor. Naming the part matters more than naming the template — a
 * deployment shipping Windows logs already has the template, and the part is
 * the checkbox they have to find.
 */
export function analysisGateMessage(
  id: AnalysisId,
  templates: TemplatesResponse | undefined,
): { title: string; hint: string } {
  const meta = ANALYSES[id]
  const found = analysisSource(id, templates)
  const template = found?.template.title ?? meta.template
  const part = found ? found.part?.title : meta.part
  return {
    title: `No source carries ${meta.label} logs yet`,
    hint: part
      ? `This page reads fields only the “${part}” part of the “${template}” template produces, and shows ` +
        `${meta.about}. Switch that part on for a source carrying the template — or add one — and this page ` +
        `fills in as its logs arrive.`
      : `This page reads fields only the “${template}” template produces, and shows ${meta.about}. Add a source ` +
        `with that template — or choose it on a source you already have — and this page fills in as its logs arrive.`,
  }
}

/**
 * What an empty window should say, naming the part that feeds the page: a page
 * that is gated open but still blank is almost always a part somebody has not
 * switched on, or a sender that has not been told to send it yet.
 */
export function analysisPartName(id: AnalysisId, templates: TemplatesResponse | undefined): string {
  return analysisSource(id, templates)?.part?.title ?? ANALYSES[id].part ?? ANALYSES[id].template
}
