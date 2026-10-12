// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Omnibar, type OmnibarNote } from './Omnibar'

vi.mock('../../api/usage-data', () => ({ recordUsageEvent: vi.fn() }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let host: HTMLDivElement

beforeEach(() => {
  vi.useFakeTimers()
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})

afterEach(() => {
  act(() => root.unmount())
  host.remove()
  document.body.innerHTML = ''
  vi.useRealTimers()
})

function render(note: OmnibarNote | undefined, { isPlaceholderData = false, isFetching = false } = {}) {
  act(() => {
    root.render(<Omnibar onOpenResource={vi.fn()} commandItems={[]} searchData={{ hits: [] }} note={note} isPlaceholderData={isPlaceholderData} isFetching={isFetching} />)
  })
}

function typeQuery(text: string) {
  const input = host.querySelector('input')!
  act(() => input.focus())
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  act(() => { vi.advanceTimersByTime(200) })
}

const findButton = (label: string) => [...document.body.querySelectorAll('button')].find((b) => b.textContent === label)

describe('Omnibar note', () => {
  it('shows the host note under the empty results and runs its action on click, then closes', () => {
    const onSelect = vi.fn()
    render({ text: "Leases can't be searched.", action: { label: 'Open the Leases list instead.', onSelect } })
    typeQuery('lease')
    expect(document.body.textContent).toContain("Leases can't be searched.")

    const action = findButton('Open the Leases list instead.')!
    const mousedown = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    act(() => { action.dispatchEvent(mousedown) })
    expect(mousedown.defaultPrevented).toBe(true)
    expect(onSelect).not.toHaveBeenCalled()

    // Keyboard activation of a focused button dispatches click too.
    act(() => action.click())
    act(() => { vi.advanceTimersByTime(500) })
    expect(onSelect).toHaveBeenCalledOnce()
    expect(host.querySelector('input')!.value).toBe('')
    expect(document.body.textContent).not.toContain("Leases can't be searched.")
  })

  it('hides a note that belongs to a previous query', () => {
    render({ text: 'Still loading — results may change.' }, { isPlaceholderData: true })
    typeQuery('lease')
    expect(document.body.textContent).not.toContain('Still loading')
  })

  it('keeps the empty state and note steady while the same query re-fetches', () => {
    render({ text: 'Still loading — results may change.' }, { isFetching: true })
    typeQuery('lease')
    expect(document.body.textContent).toContain('No resources match')
    expect(document.body.textContent).toContain('Still loading — results may change.')
  })
})
