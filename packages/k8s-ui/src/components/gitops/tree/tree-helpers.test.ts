import { describe, expect, it } from 'vitest'
import { compactInfra } from './GitOpsTreeGraph'
import { buildTreeFacets } from '../GitOpsGraphFilterRail'
import { gitOpsTreeKindLabel, matchesGitOpsTreeFilters } from './tree-helpers'
import type { GitOpsResourceTree, GitOpsTreeNode } from '../../../types'
import { getResourceIcon } from '../../../utils/resource-icons'

const node = (group: string): GitOpsTreeNode => ({ id: group, ref: { kind: 'Service', group, name: 'same', namespace: 'team' }, role: 'generated', tool: 'fluxcd' })
describe('exact group presentation', () => {
  it('separates core and Knative kind facets and filtering', () => {
    const core = node(''), knative = node('serving.knative.dev')
    const tree = { nodes: [core, knative] } as GitOpsResourceTree
    expect(buildTreeFacets(tree).kinds).toEqual([{ name: 'Service (core)', count: 1 }, { name: 'Service (serving.knative.dev)', count: 1 }])
    const filters = { kinds: new Set([gitOpsTreeKindLabel(knative.ref)]) }
    expect(matchesGitOpsTreeFilters(core, filters)).toBe(false)
    expect(matchesGitOpsTreeFilters(knative, filters)).toBe(true)
    expect(getResourceIcon(core.ref.kind, core.ref.group)).not.toBe(getResourceIcon(knative.ref.kind, knative.ref.group))
  })
  it('distinguishes a real PodGroup from the core Pod aggregate', () => {
    expect(gitOpsTreeKindLabel({ kind: 'PodGroup', group: 'scheduling.volcano.sh', name: 'jobs', namespace: 'team' })).toBe('PodGroup (scheduling.volcano.sh)')
    expect(gitOpsTreeKindLabel({ kind: 'Pod', group: '', name: 'jobs', namespace: 'team' })).toBe('Pod (core)')
  })
})

describe('compact infrastructure identity', () => {
  it('keeps canonical kinds and separates same-kind groups', () => {
    const root = { ...node('apps'), id: 'root', role: 'root' as const, ref: { kind: 'Deployment', group: 'apps', name: 'root', namespace: 'team' } }
    const children = ['', 'custom.example'].flatMap(group => [1, 2].map(i => ({ ...node(group), id: `${group}/${i}`, ref: { kind: 'ConfigMap', group, name: `config-${i}`, namespace: 'team' } })))
    const tree = { root, nodes: [root, ...children], edges: children.map(n => ({ source: root.id, target: n.id, type: 'owns' as const })), summary: { total: 5, declared: 0, generated: 4, healthy: 0, degraded: 0, unhealthy: 0, unknown: 5, byKind: {} } } as unknown as GitOpsResourceTree
    const groups = compactInfra(tree).nodes.filter(n => n.role === 'group')
    expect(groups).toHaveLength(2)
    expect(new Set(groups.map(n => n.ref.group))).toEqual(new Set(['', 'custom.example']))
    for (const group of groups) {
      expect(group.ref.kind).toBe('ConfigMap')
      expect(group.ref.name).toBe('2 ConfigMaps')
      expect(group.data?.groupedKind).toBe('ConfigMap')
    }
  })
})

it('canonicalizes built-in groups for newer UI against older Radar', () => {
  expect(gitOpsTreeKindLabel({ kind: 'Deployment', group: '', name: 'web', namespace: 'team' })).toBe('Deployment (apps)')
  expect(gitOpsTreeKindLabel({ kind: 'Widget', group: '', name: 'custom', namespace: 'team' })).toBe('Widget')
})
