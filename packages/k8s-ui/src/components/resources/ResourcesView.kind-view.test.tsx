// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
const pods: APIResource = { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const pod = { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'example', namespace: 'test', uid: 'pod-id' }, spec: { containers: [{ name: 'app', image: 'example' }] }, status: { phase: 'Running' } }
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear() })

describe('custom resource kind view', () => {
  it('keeps sidebar navigation while disabling table keys and column settings, then restores the table on null', () => {
    host = document.createElement('div'); document.body.append(host); root = createRoot(host)
    const selected = vi.fn()
    const storageRead = vi.spyOn(Storage.prototype, 'getItem')
    const storageWrite = vi.spyOn(Storage.prototype, 'setItem')
    const render = (custom: boolean) => act(() => root.render(<KeyboardShortcutProvider><ResourcesView
      namespaces={['test']}
      apiResources={[pods]}
      resourceCounts={{ Pod: 1 }}
      selectedKindQuery={{ isLoading: false, data: [pod], resourceName: 'pods', group: '' }}
      locationPathname="/resources/pods"
      locationSearch=""
      onNavigate={() => {}}
      onResourceClick={selected}
      renderKindView={kind => custom ? <div data-testid="kind-view">Custom {kind.kind}</div> : null}
    /></KeyboardShortcutProvider>))
    render(true)
    expect(host.querySelector('[data-testid="kind-view"]')?.textContent).toBe('Custom Pod')
    expect(host.querySelectorAll('table')).toHaveLength(0)
    expect(host.textContent).toContain('Workloads')
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
    expect(selected).not.toHaveBeenCalled()
    expect(storageRead.mock.calls.some(([key]) => String(key).startsWith('radar-columns-'))).toBe(false)
    expect(storageWrite.mock.calls.some(([key]) => String(key).startsWith('radar-columns-'))).toBe(false)
    render(false)
    expect(host.querySelector('[data-testid="kind-view"]')).toBeNull()
    expect(host.querySelector('table')).not.toBeNull()
    expect(storageRead.mock.calls.some(([key]) => key === 'radar-columns-pods')).toBe(true)
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
    expect(selected).toHaveBeenCalledWith(expect.objectContaining({ kind: 'pods', namespace: 'test', name: 'example' }))
  })
})
