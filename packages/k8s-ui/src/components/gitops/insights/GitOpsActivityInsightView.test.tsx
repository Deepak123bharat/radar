import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { GitOpsInsight } from '../../../types'
import { GitOpsActivityInsightView } from './GitOpsInsightViews'

function rollbackButtons(insight: GitOpsInsight): { disabled: boolean }[] {
  const html = renderToString(<GitOpsActivityInsightView insight={insight} onRollback={() => {}} />)
  return [...html.matchAll(/<button([^>]*)>Rollback<\/button>/g)].map((m) => ({ disabled: /\sdisabled=""/.test(m[1]) }))
}

function insight(autoSyncMode: string): GitOpsInsight {
  return {
    summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'guestbook', autoSyncMode },
    capabilities: { sync: true, refresh: true, terminate: true, suspend: true, resume: true, syncWithSource: false, selectiveSync: true, rollback: true },
    history: [
      { phase: 'Succeeded', revision: 'eee555' },
      { id: '2', revision: 'ccc333', rollbackable: true },
      { id: '1', revision: 'bbb222', rollbackable: false },
      { id: '0', revision: 'aaa111' },
    ],
  }
}

describe('GitOpsActivityInsightView rollback', () => {
  // The rollback API refuses an entry with no source, so its button is
  // disabled; the operation row has no id and gets no button.
  it('disables Rollback only on entries the backend marks not rollbackable', () => {
    expect(rollbackButtons(insight('Manual'))).toEqual([{ disabled: false }, { disabled: true }, { disabled: false }])
  })

  it('disables every Rollback while auto-sync is on', () => {
    expect(rollbackButtons(insight('Auto · prune'))).toEqual([{ disabled: true }, { disabled: true }, { disabled: true }])
  })
})
