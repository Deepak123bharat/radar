import { describe, expect, it, vi } from 'vitest'
import type { SearchCoverageEntry } from '../../api/client'
import { PENDING_RETRY_BUDGET_MS, kindPluralLabel, searchCoverageNote } from './searchCoverageNote'

const entry = (kind: string, reason: string, group = '', count?: number): SearchCoverageEntry => ({ kind, group, reason, count })

describe('searchCoverageNote', () => {
  it('says nothing for an older Radar or a complete search', () => {
    expect(searchCoverageNote(undefined, {})).toBeUndefined()
    expect(searchCoverageNote([], {})).toBeUndefined()
  })

  it('explains a named kind the caller or Radar cannot read', () => {
    expect(searchCoverageNote([entry('Secret', 'rbac_denied')], {})?.text).toBe("Your access doesn't include all Secrets.")
    expect(searchCoverageNote([entry('Secret', 'collector_access_denied')], {})?.text).toBe("Radar isn't allowed to read Secrets on this cluster.")
  })

  it('treats an unrecognized kind as not installed or misspelled, unless API groups went undiscovered', () => {
    expect(searchCoverageNote([entry('CiliumNetworkPolicy', 'unknown_kind')], {})?.text).toBe('No kind named CiliumNetworkPolicy here — not installed, or misspelled?')
    expect(searchCoverageNote([entry('Certificate', 'unknown_kind'), entry('*', 'read_error')], {})?.text).toBe("No kind named Certificate found, but some API groups couldn't be read.")
  })

  it('points an unsearchable kind at its list, as an action when the host can open it', () => {
    const lease = [entry('Lease', 'collection_not_supported', 'coordination.k8s.io')]
    expect(searchCoverageNote(lease, {})).toEqual({ text: "Leases can't be searched. Open the Leases list instead.", action: undefined })

    const open = vi.fn()
    const openKindList = vi.fn(() => open)
    const note = searchCoverageNote(lease, { openKindList })
    expect(note?.text).toBe("Leases can't be searched.")
    expect(note?.action?.label).toBe('Open the Leases list instead.')
    expect(openKindList).toHaveBeenCalledWith('Lease', 'coordination.k8s.io')
    note?.action?.onSelect()
    expect(open).toHaveBeenCalledOnce()
  })

  it('reports loading, then stops promising updates once retries run out', () => {
    const pending = [entry('*', 'kind_sync_pending', '', 37)]
    expect(searchCoverageNote(pending, {})?.text).toBe('Still loading — results may change.')
    expect(searchCoverageNote(pending, { pendingExhausted: true })?.text).toBe('Still loading — try again shortly.')
  })

  it('ends loading that turns into a failure on the failure, with nothing to retry', () => {
    const note = searchCoverageNote([entry('SpStall', 'kind_sync_failed', 'stall.sp-live.test')], {})
    expect(note).toEqual({ text: "Couldn't read SpStall; matches of that kind may be missing.", action: undefined })
  })

  it('names up to three failed kinds and counts the rest', () => {
    const failed = [entry('Certificate', 'kind_sync_failed'), entry('Issuer', 'read_error'), entry('*', 'read_error', '', 4)]
    expect(searchCoverageNote(failed, {})?.text).toBe("Couldn't read Certificate, Issuer and 4 more kinds; matches of those kinds may be missing.")
    expect(searchCoverageNote([entry('*', 'permission_check_failed')], {})?.text).toBe("Couldn't check your access to some kinds.")
  })

  it('counts same-named kinds of different groups apart', () => {
    const widgets = ['a', 'b', 'c', 'd', 'e'].map((g) => entry('Widget', 'kind_sync_failed', `${g}.example.io`))
    expect(searchCoverageNote([...widgets, entry('*', 'kind_sync_failed', '', 1)], {})?.text).toBe(
      "Couldn't read Widget (a.example.io), Widget (b.example.io), Widget (c.example.io) and 3 more kinds; matches of those kinds may be missing.",
    )
  })

  it('names a kind with its group when the hits hold a same-named kind of another group', () => {
    const knative = entry('Certificate', 'kind_sync_failed', 'networking.internal.knative.dev')
    expect(searchCoverageNote([knative], {})?.text).toBe("Couldn't read Certificate; matches of that kind may be missing.")
    expect(searchCoverageNote([knative], { hits: [{ kind: 'Certificate', group: 'cert-manager.io' }] })?.text).toBe(
      "Couldn't read Certificate (networking.internal.knative.dev); matches of that kind may be missing.",
    )
    expect(searchCoverageNote([entry('Certificate', 'rbac_denied', 'cert-manager.io')], { hits: [{ kind: 'Certificate', group: 'networking.internal.knative.dev' }] })?.text).toBe(
      "Your access doesn't include all Certificates (cert-manager.io).",
    )
  })

  it('says why a namespace the query named was not searched', () => {
    const ns = (reason: string, namespaces: string[]) => ({ kind: '*', group: '', reason, namespaces })
    expect(searchCoverageNote([ns('collector_scope_limited', ['rel-owner'])], {})?.text).toBe("Namespace 'rel-owner' wasn't searched: Radar here watches only some namespaces.")
    expect(searchCoverageNote([ns('namespace_scope_limited', ['billing']), ns('collector_scope_limited', ['audit'])], {})?.text).toBe("Namespaces 'audit' and 'billing' weren't searched: Radar here watches only some namespaces.")
    expect(searchCoverageNote([ns('namespace_excluded', ['payments-prd'])], { seesAllNamespaces: true })?.text).toBe("Namespace 'payments-prd' doesn't exist on this cluster.")
    expect(searchCoverageNote([ns('namespace_excluded', ['payments-prd'])], {})?.text).toBe("Namespace 'payments-prd' wasn't found in what you can see.")
  })

  it('ignores reasons it does not know', () => {
    expect(searchCoverageNote([entry('Widget', 'reason_from_the_future')], {})).toBeUndefined()
  })

  it('keeps to two sentences, loading first, and keeps the action only when its sentence shows', () => {
    const openKindList = () => () => {}
    const many = [entry('Secret', 'rbac_denied'), entry('Lease', 'collection_not_supported', 'coordination.k8s.io'), entry('*', 'kind_sync_pending')]
    expect(searchCoverageNote(many, { openKindList })?.text).toBe("Still loading — results may change. Leases can't be searched.")
    expect(searchCoverageNote([...many, entry('Deploymnet', 'unknown_kind')], { openKindList })).toEqual({
      text: 'Still loading — results may change. No kind named Deploymnet here — not installed, or misspelled?',
      action: undefined,
    })
  })
})

describe('helpers', () => {
  it('pluralizes kinds for display', () => {
    expect(['Secret', 'Ingress', 'Endpoints', 'NetworkPolicy', 'Lease'].map(kindPluralLabel)).toEqual(['Secrets', 'Ingresses', 'Endpoints', 'NetworkPolicies', 'Leases'])
  })

  it('re-queries a loading search past the point where Radar gives up on a kind', () => {
    expect(PENDING_RETRY_BUDGET_MS).toBeGreaterThan(30_000)
  })
})
