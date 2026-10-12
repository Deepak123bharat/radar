import { englishPlural, isEnglishPlural } from '@skyhook-io/k8s-ui'
import type { SearchCoverageEntry } from '../../api/client'
import type { OmnibarNote } from './Omnibar'

// While a search reports kinds still loading, the open omnibar re-queries every
// PENDING_RETRY_MS for at most PENDING_RETRY_BUDGET_MS. Radar gives up on a
// kind that never syncs after about 30 s and reports it failed, so the budget
// outlasts that and the note ends on the outcome rather than "still loading".
export const PENDING_RETRY_MS = 5000
export const PENDING_RETRY_BUDGET_MS = 45000

const FAILURE_REASONS = new Set(['kind_sync_failed', 'read_error', 'collector_unauthorized'])
const MAX_SENTENCES = 2
const MAX_NAMES = 3

export interface SearchCoverageNoteOptions {
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

// The kinds a failure reason covers, in prose: up to MAX_NAMES names, then
// "N more kinds" for the rest (named beyond the cap, or folded into a counted
// "*" entry), or "some kinds" when a "*" entry carries no count. A kind is its
// group and name: same-named kinds of different groups are named with their
// group and counted apart.
function failedKinds(entries: SearchCoverageEntry[], label: (kind: string) => string): { text: string; one: boolean } {
  const kinds = [...new Map(entries.filter((e) => e.kind !== '*').map((e) => [`${e.group}/${e.kind}`, e])).values()]
  const names = kinds.map((e) => {
    const shared = kinds.some((o) => o !== e && o.kind === e.kind)
    return shared && e.group ? `${label(e.kind)} (${e.group})` : label(e.kind)
  })
  const stars = entries.filter((e) => e.kind === '*')
  if (stars.some((e) => !e.count)) return { text: names.length ? `${names.slice(0, MAX_NAMES).join(', ')} and other kinds` : 'some kinds', one: false }
  const more = Math.max(names.length - MAX_NAMES, 0) + stars.reduce((n, e) => n + (e.count ?? 0), 0)
  const shown = names.slice(0, MAX_NAMES)
  if (!more) return { text: joinNames(shown, 'or'), one: shown.length === 1 }
  return { text: `${shown.join(', ')} and ${more} more ${more === 1 ? 'kind' : 'kinds'}`, one: false }
}

// Radar's ⌘K note for one search: what happened to the kinds the query named,
// whether results are still loading, and which kinds failed to read. Access
// and setup never come up on a broad search (Radar doesn't report them). An
// older Radar sends no `unsearched`, and gets no note.
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
  // A read_error "*" without a count beside an unknown kind: some API groups
  // failed discovery, so the kind may exist there.
  const groupsUnread = unknown.length > 0 && unsearched.some((e) => e.kind === '*' && e.reason === 'read_error' && !e.count)
  if (unknown.length) {
    const noun = `No ${unknown.length === 1 ? 'kind' : 'kinds'} named ${joinNames(unknown, 'or')}`
    say(groupsUnread ? `${noun} found, but some API groups couldn't be read.` : `${noun} here — not installed, or misspelled?`)
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
  const failed = unsearched.filter((e) => FAILURE_REASONS.has(e.reason) && !(groupsUnread && e.kind === '*' && !e.count))
  if (failed.length) {
    const { text, one } = failedKinds(failed, (kind) => kind)
    say(`Couldn't read ${text}; matches of ${one ? 'that kind' : 'those kinds'} may be missing.`)
  }
  const unchecked = unsearched.filter((e) => e.reason === 'permission_check_failed')
  if (unchecked.length) say(`Couldn't check your access to ${failedKinds(unchecked, kindPluralLabel).text}.`)

  if (!sentences.length) return undefined
  const shown = sentences.slice(0, MAX_SENTENCES)
  return { text: shown.map((s) => s.text).join(' '), action: shown.find((s) => s.action)?.action }
}

