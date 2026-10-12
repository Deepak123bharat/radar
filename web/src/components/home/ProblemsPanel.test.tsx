import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { Issue } from '@skyhook-io/k8s-ui'
import { ProblemsPanel } from './HomeView'

const line = "Helm releases couldn't be checked, so failed or stuck releases may be missing."
const issue: Issue = {
  id: 'pod-failed', severity: 'critical' as const, source: 'problem', category: 'workload', category_group: 'workload', grouping_scope: 'resource',
  kind: 'Pod', group: '', namespace: 'db', name: 'pg-1', reason: 'CrashLoopBackOff', message: 'The selected Pod keeps crashing',
}

function render(props: { issues?: Issue[]; helmUnchecked?: boolean; visibility?: { impact?: string } }) {
  const issues = props.issues ?? []
  return renderToString(
    <ProblemsPanel
      issues={issues}
      issueCount={issues.length}
      helmUnchecked={props.helmUnchecked}
      visibility={props.visibility}
      hasData
      isLoading={false}
      isRefreshing={false}
      error={null}
      totalReturned={issues.length}
      onNavigateToIssues={() => {}}
      onResourceClick={() => {}}
    />,
  ).replaceAll('<!-- -->', '').replace(/&#x27;|&#39;/g, "'")
}

describe('Home Active Issues when Radar could not read Helm releases', () => {
  it('gives no all-clear when nothing else was found', () => {
    const html = render({ helmUnchecked: true })
    expect(html).toContain('No issues found')
    expect(html).toContain(line)
    expect(html).not.toContain('No active issues')
    expect(html).not.toContain('status-healthy')
    expect(html).not.toContain('text-green-500')
  })

  it('keeps the line when limited visibility owns the empty state', () => {
    const html = render({ helmUnchecked: true, visibility: { impact: 'Pods are not readable.' } })
    expect(html).toContain('No visible active issues')
    expect(html).toContain(line)
  })

  it('says so above the rows when there are other issues', () => {
    const html = render({ issues: [issue], helmUnchecked: true })
    expect(html).toContain(line)
  })

  it('keeps the all-clear when the field is absent', () => {
    const html = render({})
    expect(html).toContain('No active issues')
    expect(html).toContain('status-healthy')
    expect(html).not.toContain('couldn')
  })
})
