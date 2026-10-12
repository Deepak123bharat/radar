import { describe, expect, it, vi } from 'vitest'
import type { SearchCoverageEntry } from '../../api/client'
import { kindPluralLabel, queryNamesKind, searchCoverageNote } from './searchCoverageNote'

const entry = (kind: string, reason: string, group = ''): SearchCoverageEntry => ({ kind, group, reason })
const named = { namedKinds: true }
const broad = { namedKinds: false }

describe('searchCoverageNote', () => {
  it('says nothing for an older Radar or a complete search', () => {
    expect(searchCoverageNote(undefined, named)).toBeUndefined()
    expect(searchCoverageNote([], named)).toBeUndefined()
  })

  it('explains a named kind the caller or Radar cannot read', () => {
    expect(searchCoverageNote([entry('Secret', 'rbac_denied')], named)?.text).toBe("Your access doesn't include all Secrets.")
    expect(searchCoverageNote([entry('Secret', 'collector_access_denied')], named)?.text).toBe("Radar isn't allowed to read Secrets on this cluster.")
  })

  it('flags a typo', () => {
    expect(searchCoverageNote([entry('Deploymnet', 'unknown_kind')], named)?.text).toBe('No kind named Deploymnet. Check the spelling.')
  })

  it('points an unsearchable kind at its list, as an action when the host can open it', () => {
    const lease = [entry('Lease', 'collection_not_supported', 'coordination.k8s.io')]
    expect(searchCoverageNote(lease, named)).toEqual({ text: "Leases can't be searched. Open the Leases list instead.", action: undefined })

    const open = vi.fn()
    const openKindList = vi.fn(() => open)
    const note = searchCoverageNote(lease, { ...named, openKindList })
    expect(note?.text).toBe("Leases can't be searched.")
    expect(note?.action?.label).toBe('Open the Leases list instead.')
    expect(openKindList).toHaveBeenCalledWith('Lease', 'coordination.k8s.io')
    note?.action?.onSelect()
    expect(open).toHaveBeenCalledOnce()
  })

  it('reports loading on any search, then stops promising updates once retries run out', () => {
    const pending = [{ kind: '*', group: '', reason: 'kind_sync_pending', count: 37 }]
    expect(searchCoverageNote(pending, broad)?.text).toBe('Still loading — results may change.')
    expect(searchCoverageNote(pending, { ...broad, pendingExhausted: true })?.text).toBe('Still loading — try again shortly.')
  })

  it('names failures only for kinds the query named', () => {
    const failed = [entry('Certificate', 'kind_sync_failed', 'cert-manager.io'), { kind: '*', group: '', reason: 'read_error', count: 4 }]
    expect(searchCoverageNote(failed, broad)).toBeUndefined()
    expect(searchCoverageNote(failed, named)?.text).toBe("Couldn't read Certificate.")
  })

  it('leaves namespaces and unknown reasons to other surfaces', () => {
    expect(searchCoverageNote([{ kind: '*', group: '', reason: 'namespace_excluded', namespaces: ['payments'] }], named)).toBeUndefined()
    expect(searchCoverageNote([entry('Widget', 'reason_from_the_future')], named)).toBeUndefined()
  })

  it('keeps to two sentences, loading first, and keeps the action only when its sentence shows', () => {
    const openKindList = () => () => {}
    const many = [entry('Secret', 'rbac_denied'), entry('Lease', 'collection_not_supported', 'coordination.k8s.io'), { kind: '*', group: '', reason: 'kind_sync_pending' }]
    expect(searchCoverageNote(many, { ...named, openKindList })?.text).toBe("Still loading — results may change. Leases can't be searched.")
    expect(searchCoverageNote([...many, entry('Deploymnet', 'unknown_kind')], { ...named, openKindList })).toEqual({
      text: 'Still loading — results may change. No kind named Deploymnet. Check the spelling.',
      action: undefined,
    })
  })
})

describe('helpers', () => {
  it('pluralizes kinds for display', () => {
    expect(['Secret', 'Ingress', 'Endpoints', 'NetworkPolicy', 'Lease'].map(kindPluralLabel)).toEqual(['Secrets', 'Ingresses', 'Endpoints', 'NetworkPolicies', 'Leases'])
  })

  it('detects a kind: modifier', () => {
    expect(queryNamesKind('kind:Secret db')).toBe(true)
    expect(queryNamesKind('ns:a KIND:Pod')).toBe(true)
    expect(queryNamesKind('k:Secret')).toBe(true)
    expect(queryNamesKind('mykind:x db')).toBe(false)
    expect(queryNamesKind('db')).toBe(false)
  })
})
