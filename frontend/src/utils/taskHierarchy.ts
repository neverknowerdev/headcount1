export interface HierarchyTask {
  id: number;
  title: string;
  ref_key?: string;
  parent_id?: number | null;
}

export interface TaskNode<T extends HierarchyTask> {
  task: T;
  children: TaskNode<T>[];
  orphanParentId?: number;
}

/** Builds a stable forest. Missing parents become visible roots and cycles are
 * broken at the smallest id in each cycle, so malformed data cannot hide work. */
export function buildTaskForest<T extends HierarchyTask>(tasks: T[]): TaskNode<T>[] {
  const nodes = new Map<number, TaskNode<T>>();
  const parentById = new Map<number, number | null>();
  for (const task of tasks) {
    nodes.set(task.id, { task, children: [] });
    parentById.set(task.id, task.parent_id ?? null);
  }

  // Parent edges form a functional graph. Walk each unvisited chain once and
  // sever one deterministic edge when a cycle is found (linear overall time).
  const state = new Map<number, 0 | 1 | 2>();
  for (const startId of nodes.keys()) {
    if (state.get(startId)) continue;
    const path: number[] = [];
    const position = new Map<number, number>();
    let id: number | null = startId;
    while (id != null && nodes.has(id) && !state.get(id)) {
      state.set(id, 1);
      position.set(id, path.length);
      path.push(id);
      const parentId = parentById.get(id);
      id = parentId != null && nodes.has(parentId) && parentId !== id ? parentId : null;
    }
    if (id != null && state.get(id) === 1 && position.has(id)) {
      const cycle = path.slice(position.get(id)!);
      const breakId = cycle.reduce((smallest, current) => Math.min(smallest, current));
      parentById.set(breakId, null);
    }
    for (const pathId of path) state.set(pathId, 2);
  }

  const roots: TaskNode<T>[] = [];
  for (const task of tasks) {
    const node = nodes.get(task.id)!;
    const parentId = parentById.get(task.id);
    if (parentId != null && nodes.has(parentId) && parentId !== task.id) {
      nodes.get(parentId)!.children.push(node);
    } else {
      if (task.parent_id != null && task.parent_id !== task.id && !nodes.has(task.parent_id)) {
        node.orphanParentId = task.parent_id;
      }
      roots.push(node);
    }
  }
  return roots;
}

export function getSearchVisibility<T extends HierarchyTask>(tasks: T[], query: string) {
  const normalized = query.trim().toLocaleLowerCase();
  if (!normalized) return { visibleIds: null as Set<number> | null, expandedIds: new Set<number>() };
  const byId = new Map(tasks.map(task => [task.id, task]));
  const visibleIds = new Set<number>();
  const expandedIds = new Set<number>();
  for (const task of tasks) {
    if (!`${task.title} ${task.ref_key ?? ''}`.toLocaleLowerCase().includes(normalized)) continue;
    visibleIds.add(task.id);
    let parentId = task.parent_id ?? null;
    const seen = new Set<number>([task.id]);
    while (parentId != null && byId.has(parentId) && !seen.has(parentId)) {
      seen.add(parentId);
      visibleIds.add(parentId);
      expandedIds.add(parentId);
      parentId = byId.get(parentId)!.parent_id ?? null;
    }
  }
  return { visibleIds, expandedIds };
}
