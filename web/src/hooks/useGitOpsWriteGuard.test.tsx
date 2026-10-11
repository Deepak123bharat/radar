// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { SetImageDialog, canConfirmGitOpsWrite, type WorkloadImageInventory } from '@skyhook-io/k8s-ui'
import { afterEach, expect, it, vi } from 'vitest'
import { ApplyDialog } from '../components/diagnose/ApplyDialog'
import { useGitOpsWriteGuard, type GitOpsWriteGuardState } from './useGitOpsWriteGuard'

const mocks = vi.hoisted(() => ({
  applications: { data: undefined as unknown[] | undefined, isPending: true, isError: false },
  evidence: vi.fn(),
  target: { data: undefined as unknown, relationships: undefined as Record<string, unknown> | undefined, isLoading: false, isError: false },
}))
vi.mock('../api/client', () => ({
  useResource: (kind: string) => kind === 'deployments' ? mocks.target : ({ data: undefined, isLoading: false, isError: false }),
  useResourceWithRelationships: () => ({ data: undefined, isPending: false, isError: false }),
  useResources: () => mocks.applications,
  useRadarFeature: () => ({ guard: (fn: () => unknown) => fn(), gatedKey: [] }),
  fetchGitOpsWriteEvidence: (...args: unknown[]) => mocks.evidence(...args),
}))

afterEach(() => {
  mocks.applications.data = undefined
  mocks.applications.isPending = true
  mocks.applications.isError = false
  mocks.evidence.mockReset()
  Object.assign(mocks.target, { data: undefined, relationships: undefined, isLoading: false, isError: false })
})

it.each(['resolved', 'failed', 'empty', 'ambiguous'])('waits for Argo Application namespace lookup before handling %s ownership', async (outcome) => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  let state: GitOpsWriteGuardState
  const onLoad = vi.fn(async (): Promise<WorkloadImageInventory> => ({
    target: { kind: 'Deployment', group: 'apps', resource: 'deployments', namespace: 'prod', name: 'web' },
    containers: [{ type: 'container', name: 'app', image: 'app:old' }],
    behavior: { type: 'rolling' },
  }))
  mocks.evidence.mockResolvedValue({ uid: 'deployment', resourceVersion: '1', owner: { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'web' }, policy: null, paths: [] })
  function Harness() {
    state = useGitOpsWriteGuard({
      target: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' },
      resource: {},
      relationships: { managedBy: [{ kind: 'Application', group: 'argoproj.io', namespace: '', name: 'web' }] },
      writes: [{ scope: 'spec', paths: ['spec.replicas'] }],
    })
    return outcome !== 'resolved' ? <SetImageDialog open workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={state.ownership.lookupError || !state.guard ? undefined : { guard: state.guard }} onLoad={onLoad} onClose={() => {}} onConfirm={async () => {}} /> : null
  }
  const render = () => root.render(<QueryClientProvider client={queryClient}><Harness /></QueryClientProvider>)
  try {
    await act(async () => render())
    expect(state!.guard?.pending).toBe(true)
    expect(mocks.evidence).not.toHaveBeenCalled()
    mocks.applications.isPending = false
    if (outcome === 'resolved') mocks.applications.data = [{ metadata: { name: 'web', namespace: 'argocd' } }]
    else if (outcome === 'failed') mocks.applications.isError = true
    else mocks.applications.data = outcome === 'empty' ? [] : ['argocd-a', 'argocd-b'].map((namespace) => ({ metadata: { name: 'web', namespace } }))
    await act(async () => render())
    if (outcome === 'resolved') {
      await act(async () => vi.waitFor(() => expect(mocks.evidence).toHaveBeenCalledTimes(1)))
      expect(mocks.evidence.mock.calls[0][0].owner.namespace).toBe('argocd')
    } else {
      expect(mocks.evidence).not.toHaveBeenCalled()
      expect(state!.ownership.lookupError).toBe(false)
      expect(state!.guard?.pending).toBe(false)
      expect(state!.guard?.requiresAck).toBe(true)
      expect(state!.guard?.level).toBe('may-revert')
      expect(canConfirmGitOpsWrite(state!.guard, true)).toBe(true)
      expect(document.body.textContent).not.toContain('Management ownership unavailable')
      expect(document.body.textContent).toContain('Argo CD')
    }
  } finally {
    await act(async () => root.unmount())
    queryClient.clear()
  }
})


it.each([undefined, {}])('keeps server-detected management as a floor when relationships are %s', async (relationships) => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  mocks.target.data = { kind: 'Deployment', metadata: { name: 'web' } }
  mocks.target.relationships = relationships
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const host = document.createElement('div'); const root = createRoot(host)
  let state: GitOpsWriteGuardState
  function Harness() {
    state = useGitOpsWriteGuard({ target: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' }, writes: [{ scope: 'spec' }], managedBy: 'Argo CD' })
    return <ApplyDialog open onClose={() => {}} onConfirm={() => {}} agentLabel="Local agent" resourceLabel="Deployment prod/web" context="test" gitOpsGuard={state.guard} />
  }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>))
    expect(state!.guard?.owner).toBeNull()
    expect(state!.guard?.level).toBe('may-revert')
    expect(state!.guard?.requiresAck).toBe(true)
    expect(document.body.textContent).toContain('previously identified Argo CD')
    const confirm = [...document.body.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('Apply fix'))!
    expect(confirm.disabled).toBe(true)
    const ack = document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    expect(ack).not.toBeNull()
    await act(async () => ack.click())
    expect(confirm.disabled).toBe(false)
    expect(mocks.evidence).not.toHaveBeenCalled()
  } finally {
    await act(async () => root.unmount()); client.clear()
  }
})

it('uses a conservative acknowledgement when the loaded target has no relationships', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  mocks.target.data = { kind: 'Deployment' }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  let state: GitOpsWriteGuardState
  function Harness() {
    state = useGitOpsWriteGuard({ target: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' }, writes: [{ scope: 'spec' }] })
    return null
  }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>))
    expect(state!.guard?.level).toBe('may-revert')
    expect(state!.guard?.requiresAck).toBe(true)
    expect(canConfirmGitOpsWrite(state!.guard, false)).toBe(false)
    expect(canConfirmGitOpsWrite(state!.guard, true)).toBe(true)
  } finally { await act(async () => root.unmount()); client.clear() }
})

it('refreshes evidence on each image dialog open and Refresh, blocking Update until settled', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  const owner = { tool: 'fluxcd' as const, kind: 'kustomizations' as const, namespace: 'flux-system', name: 'web' }
  const ownership = { owner, rawOwner: owner, helmOwner: null, ownerObject: {}, ownerPending: false, ownerVerified: true, ownerStatus: null, ownerSource: null, helmSource: null, inheritedLookupRef: null, lookupPending: false, lookupError: false }
  const inventory: WorkloadImageInventory = { target: { kind: 'Deployment', group: 'apps', resource: 'deployments', namespace: 'prod', name: 'web' }, containers: [{ type: 'container', name: 'app', image: 'app:old' }], behavior: { type: 'rolling' } }
  const path = 'spec.template.spec.containers[*].image'
  const evidence = (ignore: boolean) => ({ uid: 'u', resourceVersion: ignore ? '1' : '2', owner: null, policy: { tool: 'fluxcd', auto: true, selfHeal: true, prune: true, suspended: false, objectReconcile: ignore ? 'ignore' : undefined }, paths: [{ path, lastApplied: 'present', ownedBy: [], ownedByGitOps: true }] })
  const resolvers: ((value: ReturnType<typeof evidence>) => void)[] = []
  mocks.evidence.mockImplementation(() => new Promise((resolve) => resolvers.push(resolve)))
  const close = vi.fn()
  let state: GitOpsWriteGuardState
  function Harness({ open }: { open: boolean }) {
    const [requested, setRequested] = useState(false)
    state = useGitOpsWriteGuard({ target: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' }, writes: [{ scope: 'spec', paths: [path] }], ownership, enabled: requested })
    return <SetImageDialog open={open} workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={state.guard ? { guard: state.guard } : undefined} onLoad={async () => { setRequested(true); await state.refresh(); return inventory }} onClose={close} onConfirm={async () => {}} />
  }
  const render = (open: boolean) => root.render(<QueryClientProvider client={client}><Harness open={open} /></QueryClientProvider>)
  const update = () => [...document.body.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.className.includes('btn-brand ') && button.closest('.dialog')) ?? document.body.querySelector<HTMLButtonElement>('button.btn-brand')!
  try {
    await act(async () => render(true))
    await vi.waitFor(() => expect(mocks.evidence).toHaveBeenCalledTimes(1))
    expect(update().disabled).toBe(true)
    const dismiss = document.body.querySelector<HTMLButtonElement>('button[aria-label="Close"]')!
    expect(dismiss.disabled).toBe(false)
    expect([...document.body.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.trim() === 'Cancel')!.disabled).toBe(false)
    await act(async () => dismiss.click())
    expect(close).toHaveBeenCalledTimes(1)
    await act(async () => { resolvers[0](evidence(true)); await new Promise((resolve) => setTimeout(resolve, 0)) })
    expect(state!.guard?.level).toBe('info')
    await act(async () => render(false))
    await act(async () => render(true))
    await vi.waitFor(() => expect(mocks.evidence).toHaveBeenCalledTimes(2))
    expect(state!.guard?.pending).toBe(true)
    expect(update().disabled).toBe(true)
    await act(async () => { resolvers[1](evidence(false)); await new Promise((resolve) => setTimeout(resolve, 0)) })
    expect(state!.guard?.level).toBe('will-revert')
    const input = document.body.querySelector<HTMLInputElement>('[aria-label="New image for app"]')!
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'app:new'); input.dispatchEvent(new Event('input', { bubbles: true })) })
    const ack = document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    await act(async () => ack.click())
    expect(update().disabled).toBe(false)
    const refresh = [...document.body.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.trim() === 'Refresh')!
    await act(async () => refresh.click())
    await vi.waitFor(() => expect(mocks.evidence).toHaveBeenCalledTimes(3))
    expect(document.body.querySelector<HTMLButtonElement>('button[aria-label="Close"]')!.disabled).toBe(false)
    expect(state!.guard?.pending).toBe(true)
    expect(update().disabled).toBe(true)
    await act(async () => { resolvers[2](evidence(false)); await new Promise((resolve) => setTimeout(resolve, 0)) })
    expect(update().disabled).toBe(true)
    expect(document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false)
    mocks.evidence.mockResolvedValueOnce(evidence(true))
    await act(async () => { refresh.click(); await new Promise((resolve) => setTimeout(resolve, 0)) })
    await act(async () => vi.waitFor(() => expect(state!.guard?.pending).toBe(false)))
    expect(mocks.evidence).toHaveBeenCalledTimes(4)
    expect(state!.guard?.level).toBe('info')
    mocks.evidence.mockRejectedValueOnce(new Error('evidence refresh failed'))
    await act(async () => { refresh.click(); await new Promise((resolve) => setTimeout(resolve, 0)) })
    await act(async () => vi.waitFor(() => expect(state!.guard?.pending).toBe(false)))
    expect(mocks.evidence).toHaveBeenCalledTimes(5)
    expect(state!.guard?.level).toBe('may-revert')
    expect(state!.guard?.requiresAck).toBe(true)
    expect(update().disabled).toBe(true)
  } finally { await act(async () => root.unmount()); client.clear() }
})


it('lets an image edit proceed with acknowledgement when ownership is unavailable, and retry it with Refresh', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  mocks.target.data = { kind: 'Deployment' }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRoot(document.createElement('div'))
  const retry = vi.fn()
  let state: GitOpsWriteGuardState
  function Harness() {
    const [, rerender] = useState(0)
    state = useGitOpsWriteGuard({ target: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'web' }, writes: [{ scope: 'spec' }] })
    return <SetImageDialog open workloadLabel="Deployment" workloadName="web" workloadResource="deployments" ownership={state.guard ? { guard: state.guard } : undefined} onLoad={async () => { retry(); if (retry.mock.calls.length > 1) mocks.target.relationships = {}; rerender((value) => value + 1); return { target: { kind: 'Deployment', group: 'apps', resource: 'deployments', namespace: 'prod', name: 'web' }, containers: [{ type: 'container', name: 'app', image: 'app:old' }], behavior: { type: 'rolling' } } }} onClose={() => {}} onConfirm={async () => {}} />
  }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>))
    expect(state!.guard?.level).toBe('may-revert')
    expect(document.body.textContent).not.toContain('Management ownership unavailable')
    expect(document.body.textContent).toContain('may revert')
    const input = document.body.querySelector<HTMLInputElement>('[aria-label="New image for app"]')!
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'app:new'); input.dispatchEvent(new Event('input', { bubbles: true })) })
    const update = document.body.querySelector<HTMLButtonElement>('button.btn-brand')!
    expect(update.disabled).toBe(true)
    await act(async () => document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click())
    expect(update.disabled).toBe(false)
    const refresh = [...document.body.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.trim() === 'Refresh')!
    await act(async () => refresh.click())
    expect(retry).toHaveBeenCalledTimes(2)
    expect(state!.guard?.level).toBe('none')
    expect(update.disabled).toBe(false)
  } finally { await act(async () => root.unmount()); client.clear() }
})
