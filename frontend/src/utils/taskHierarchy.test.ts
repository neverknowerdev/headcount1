import { describe, expect, it } from 'vitest';
import { buildTaskForest, getSearchVisibility } from './taskHierarchy';

const tasks = [
  { id: 10, title: 'Root work', ref_key: 'APP-10', parent_id: null },
  { id: 11, title: 'Investigate provider', ref_key: 'APP-11', parent_id: 10 },
  { id: 12, title: 'Follow up', ref_key: 'APP-12', parent_id: 11 },
  { id: 14, title: 'Missing parent child', parent_id: 13 },
];

describe('task hierarchy helpers', () => {
  it('builds a stable tree and keeps tasks with missing parents as roots with context', () => {
    const forest = buildTaskForest(tasks);
    expect(forest.map(node => node.task.id)).toEqual([10, 14]);
    expect(forest[0].children[0].children[0].task.id).toBe(12);
    expect(forest[1].orphanParentId).toBe(13);
  });

  it('breaks self and multi-node cycles without dropping any task', () => {
    const forest = buildTaskForest([
      { id: 20, title: 'Self', parent_id: 20 },
      { id: 21, title: 'Cycle A', parent_id: 22 },
      { id: 22, title: 'Cycle B', parent_id: 21 },
    ]);
    const seen: number[] = [];
    const visit = (nodes: typeof forest) => nodes.forEach(node => { seen.push(node.task.id); visit(node.children); });
    visit(forest);
    expect(seen.sort()).toEqual([20, 21, 22]);
    expect(forest.map(node => node.task.id)).toContain(21);
  });

  it('searches task title and reference and includes ancestors to preserve context', () => {
    const result = getSearchVisibility(tasks, 'APP-12');
    expect([...result.visibleIds!].sort()).toEqual([10, 11, 12]);
    expect([...result.expandedIds].sort()).toEqual([10, 11]);
    expect(getSearchVisibility(tasks, 'nothing').visibleIds?.size).toBe(0);
    expect(getSearchVisibility(tasks, '').visibleIds).toBeNull();
  });
});
