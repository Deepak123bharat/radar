import { englishPlural, isEnglishPlural } from '@skyhook-io/k8s-ui'
import type { SearchCoverageEntry } from '../../api/client'
import type { OmnibarNote } from './Omnibar'

// While a search reports kinds still loading, the open omnibar re-queries every
// PENDING_RETRY_MS for at most PENDING_RETRY_BUDGET_MS.
export const PENDING_RETRY_MS = 5000
export const PENDING_RETRY_BUDGET_MS = 30000

const FAILURE_REASONS = new Set(['kind_sync_failed', 'read_error', 'collector_unauthorized'])
const MAX_SENTENCES = 2

export interface SearchCoverageNoteOptions {
  /** The query has a kind: term. Failures are shown only for kinds the user named. */
  namedKinds: boolean
  /** Re-querying a still-loading search ran out of budget. */
  pendingExhausted?: boolean
  /** Returns how to open a kind's resource list, or undefined when the host can't. */
  openKindList?: (kind: string, group: string) => (() => void) | undefined
}

/** Plural display form of a Kind name ("Secrets", "Ingresses", "Endpoints"). */
export function kindPluralLabel(kind: string): string {
  return isEnglishPlural(kind) ? kind : englishPlural(kind)
}

function joinNames(names: string[], conj: string): string {
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} ${conj} ${names[names.length - 1]}`
}

// Radar's ⌘K note for one search: what happened to the kinds the query named,
// and whether results are still loading. Access and setup never come up on a
// broad search (Radar doesn't report them); broad failures stay quiet here too.
// An older Radar sends no `unsearched`, and gets no note.
export function searchCoverageNote(unsearched: SearchCoverageEntry[] | undefined, opts: SearchCoverageNoteOptions): OmnibarNote | undefined {
  if (!unsearched?.length) return undefined
  const named = (reasons: string | Set<string>) =>
    [...new Set(unsearched.filter((e) => e.kind !== '*' && (typeof reasons === 'string' ? e.reason === reasons : reasons.has(e.reason))).map((e) => e.kind))]

  const sentences: OmnibarNote[] = []
  const say = (text: string) => sentences.push({ text })

  if (unsearched.some((e) => e.reason === 'kind_sync_pending')) {
    say(opts.pendingExhausted ? 'Still loading — try again shortly.' : 'Still loading — results may change.')
  }
  const unknown = named('unknown_kind')
  if (unknown.length) {
    const noun = `No ${unknown.length === 1 ? 'kind' : 'kinds'} named ${joinNames(unknown, 'or')}`
    // A read_error "*" without a count: some API groups failed discovery, so
    // the kind may exist there.
    const groupsUnread = unsearched.some((e) => e.kind === '*' && e.reason === 'read_error' && !e.count)
    say(groupsUnread ? `${noun} found, but some API groups couldn't be read.` : `${noun}. Check the spelling.`)
  }
  const unsupported = unsearched.filter((e) => e.kind !== '*' && e.reason === 'collection_not_supported')
  if (unsupported.length) {
    const first = unsupported[0]
    const label = kindPluralLabel(first.kind)
    const names = joinNames([...new Set(unsupported.map((e) => kindPluralLabel(e.kind)))], 'and')
    const pointer = `Open the ${label} list instead.`
    const openList = opts.openKindList?.(first.kind, first.group)
    sentences.push(openList
      ? { text: `${names} can't be searched.`, action: { label: pointer, onSelect: openList } }
      : { text: `${names} can't be searched. ${pointer}` })
  }
  const denied = named('rbac_denied')
  if (denied.length) say(`Your access doesn't include all ${joinNames(denied.map(kindPluralLabel), 'or')}.`)
  const collector = named('collector_access_denied')
  if (collector.length) say(`Radar isn't allowed to read ${joinNames(collector.map(kindPluralLabel), 'or')} on this cluster.`)
  if (opts.namedKinds) {
    const failed = named(FAILURE_REASONS)
    if (failed.length) say(`Couldn't read ${joinNames(failed, 'or')}.`)
    const unchecked = named('permission_check_failed')
    if (unchecked.length) say(`Couldn't check your access to ${joinNames(unchecked.map(kindPluralLabel), 'or')}.`)
  }

  if (!sentences.length) return undefined
  const shown = sentences.slice(0, MAX_SENTENCES)
  return { text: shown.map((s) => s.text).join(' '), action: shown.find((s) => s.action)?.action }
}

/** Whether a query string names a kind with a `kind:` (or `k:`) modifier. */
export function queryNamesKind(query: string): boolean {
  return /(^|\s)(kind|k):\S/i.test(query)
}
