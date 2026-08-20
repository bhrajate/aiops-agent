import { describe, it, expect } from 'vitest'
import { unwrapList } from './endpoints'

// unwrapList 是所有列表页的入口。它对不上形状时**不报错**,只返回空数组 ——
// 界面显示"暂无数据",而这与"确实没有数据"看起来完全一样。
// 这些断言锁住每个真实端点的包裹键名。
describe('unwrapList', () => {
  it('裸数组直接返回', () => {
    expect(unwrapList([1, 2, 3])).toEqual([1, 2, 3])
    expect(unwrapList([])).toEqual([])
  })

  it('认识每个真实端点的包裹键', () => {
    // 键名取自 control-plane 各 handler 的 httpx.JSON 调用。
    // 新增列表端点时必须同步 LIST_KEYS,否则那个页面永远空着且不报错。
    const cases: Array<[string, unknown]> = [
      ['incidents', { incidents: [{ id: 1 }] }], // listIncidents
      ['investigations', { investigations: [{ id: 1 }], count: 1 }], // listInvestigations
      ['golden_cases', { golden_cases: [{ id: 1 }], status: 'pending' }], // listGoldenCases
      ['items', { items: [{ id: 1 }] }], // searchKnowledge
      ['entries', { entries: [{ id: 1 }], next_cursor: 0 }], // listAudit
    ]
    for (const [name, payload] of cases) {
      expect(unwrapList(payload), `键 ${name} 未被识别`).toHaveLength(1)
    }
  })

  it('空列表与缺字段都得到空数组(不抛错)', () => {
    expect(unwrapList({ incidents: [] })).toEqual([])
    expect(unwrapList({})).toEqual([])
    expect(unwrapList(null)).toEqual([])
    expect(unwrapList(undefined)).toEqual([])
    expect(unwrapList('nope')).toEqual([])
    expect(unwrapList(42)).toEqual([])
  })

  it('后端返回 incidents:null 时不崩(ABAC 过滤到空的历史行为)', () => {
    // Go 里 `visible := incs[:0]` 在无匹配时会序列化成 [] 而非 null,
    // 但早期版本与其他 handler 可能返回 null。不能让它抛错。
    expect(unwrapList({ incidents: null })).toEqual([])
  })

  it('多个候选键共存时取第一个匹配的', () => {
    // 不该悄悄合并两个列表 —— 那会让计数翻倍。
    const out = unwrapList({ items: [1], data: [2, 3] })
    expect(out).toEqual([1])
  })
})
