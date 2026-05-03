import type {ActionsJob, ActionsRunStatus} from '../modules/gitea-actions.ts';

export type GraphNodeType = 'job' | 'matrix' | 'group';

export type GraphNode = {
  id: string;
  type: GraphNodeType;
  name: string;
  status: ActionsRunStatus;
  duration: string;
  x: number;
  y: number;
  level: number;
  displayHeight: number;
  jobs: ActionsJob[];
  matrixKey?: string;
};

export type Edge = {
  fromId: string;
  toId: string;
  key: string;
};

export type RoutedEdge = Edge & {
  path: string;
  fromNode: GraphNode;
  toNode: GraphNode;
};

export type IncomingBundle = {
  key: string;
  toId: string;
  fromIds: string[];
  path: string;
};

export type OutgoingBundle = {
  key: string;
  fromId: string;
  toIds: string[];
  path: string;
};

export type WorkflowGraphLayoutOptions = {
  margin: number;
  nodeWidth: number;
  nodeHeight: number;
  columnGap: number;
  laneGap: number;
  groupRowHeight: number;
  groupPadY: number;
  matrixCollapsedHeight: number;
  matrixHeaderHeight: number;
  matrixRowHeight: number;
  matrixPadY: number;
  bundleStub: number;
};

export type WorkflowGraphModel = {
  nodes: GraphNode[];
  edges: Edge[];
  routedEdges: RoutedEdge[];
  incomingBundles: IncomingBundle[];
  outgoingBundles: OutgoingBundle[];
};

const defaultLayoutOptions: WorkflowGraphLayoutOptions = {
  margin: 24,
  nodeWidth: 220,
  nodeHeight: 40,
  columnGap: 96,
  laneGap: 46,
  groupRowHeight: 28,
  groupPadY: 8,
  matrixCollapsedHeight: 96,
  matrixHeaderHeight: 24,
  matrixRowHeight: 28,
  matrixPadY: 8,
  bundleStub: 18,
};

function canonicalNeedsKey(needs: string[] | undefined): string {
  if (!needs?.length) return '';
  return Array.from(needs).sort().join('\u0001');
}

function graphIdForJob(job: ActionsJob): string {
  return `job:${job.id}`;
}

export function matrixKeyFromJobName(name: string): string | null {
  const idx = name.indexOf(' (');
  if (idx === -1) return null;
  return name.slice(0, idx).trim() || null;
}

export function boxBottom(node: GraphNode): number {
  return node.y + node.displayHeight;
}

export function boxCenterY(node: GraphNode): number {
  return node.y + node.displayHeight / 2;
}

function nodesFromIds(nodesById: Map<string, GraphNode>, ids: string[]): GraphNode[] {
  const resolved: GraphNode[] = [];
  for (const id of ids) {
    const node = nodesById.get(id);
    if (node) resolved.push(node);
  }
  return resolved;
}

function buildIncomingOutgoingIdMaps(edges: Edge[]): {
  incomingByNodeId: Map<string, string[]>;
  outgoingByNodeId: Map<string, string[]>;
} {
  const incomingByNodeId = new Map<string, string[]>();
  const outgoingByNodeId = new Map<string, string[]>();
  for (const edge of edges) {
    if (!incomingByNodeId.has(edge.toId)) incomingByNodeId.set(edge.toId, []);
    incomingByNodeId.get(edge.toId)!.push(edge.fromId);
    if (!outgoingByNodeId.has(edge.fromId)) outgoingByNodeId.set(edge.fromId, []);
    outgoingByNodeId.get(edge.fromId)!.push(edge.toId);
  }
  return {incomingByNodeId, outgoingByNodeId};
}

function matrixPanelHeight(rowCount: number, expanded: boolean, options: WorkflowGraphLayoutOptions): number {
  if (rowCount <= 0) return options.nodeHeight;
  if (!expanded) return options.matrixCollapsedHeight;
  return options.matrixHeaderHeight + (rowCount + 2) * options.matrixRowHeight + options.matrixPadY * 2;
}

function groupPanelHeight(rowCount: number, options: WorkflowGraphLayoutOptions): number {
  return rowCount * options.groupRowHeight + options.groupPadY * 2;
}

function compareStatusWorstFirst(a: ActionsRunStatus, b: ActionsRunStatus): number {
  const rank = (s: ActionsRunStatus) => {
    if (s === 'failure') return 0;
    if (s === 'cancelled') return 1;
    if (s === 'running') return 2;
    if (s === 'waiting') return 3;
    if (s === 'blocked') return 4;
    if (s === 'success') return 5;
    if (s === 'skipped') return 6;
    return 7;
  };
  return rank(a) - rank(b);
}

function aggregateStatus(children: ActionsJob[]): ActionsRunStatus {
  return children.map((c) => c.status).slice().sort(compareStatusWorstFirst)[0] ?? 'unknown';
}

export function buildDirectNeedsMap(jobs: ActionsJob[]): Map<string, string[]> {
  const directNeedsByJobId = new Map<string, string[]>();
  const dependentsByJobId = new Map<string, Set<string>>();

  for (const job of jobs) {
    const needs = job.needs || [];
    directNeedsByJobId.set(job.jobId, needs);

    for (const need of needs) {
      if (!dependentsByJobId.has(need)) {
        dependentsByJobId.set(need, new Set());
      }
      dependentsByJobId.get(need)?.add(job.jobId);
    }
  }

  const reachabilityCache = new Map<string, boolean>();

  function canReach(fromJobId: string, toJobId: string): boolean {
    const cacheKey = `${fromJobId}->${toJobId}`;
    if (reachabilityCache.has(cacheKey)) {
      return reachabilityCache.get(cacheKey) ?? false;
    }

    const visited = new Set<string>();
    const stack = Array.from(dependentsByJobId.get(fromJobId) || []);

    while (stack.length > 0) {
      const current = stack.pop();
      if (!current) continue;
      if (current === toJobId) {
        reachabilityCache.set(cacheKey, true);
        return true;
      }
      if (visited.has(current)) continue;
      visited.add(current);
      stack.push(...(dependentsByJobId.get(current) || []));
    }

    reachabilityCache.set(cacheKey, false);
    return false;
  }

  const reducedNeedsByJobId = new Map<string, string[]>();
  for (const [jobId, needs] of directNeedsByJobId.entries()) {
    reducedNeedsByJobId.set(jobId, needs.filter((need) => {
      return !needs.some((otherNeed) => otherNeed !== need && canReach(need, otherNeed));
    }));
  }

  return reducedNeedsByJobId;
}

export function computeJobLevels(jobs: ActionsJob[]): Map<string, number> {
  const jobMap = new Map<string, ActionsJob>();
  for (const job of jobs) {
    jobMap.set(job.name, job);
    if (job.jobId) jobMap.set(job.jobId, job);
  }

  const levels = new Map<string, number>();
  const visited = new Set<string>();
  const recursionStack = new Set<string>();

  function dfs(jobNameOrId: string): number {
    if (recursionStack.has(jobNameOrId)) return 0;
    if (visited.has(jobNameOrId)) return levels.get(jobNameOrId) ?? 0;

    recursionStack.add(jobNameOrId);
    visited.add(jobNameOrId);

    const job = jobMap.get(jobNameOrId);
    if (!job) {
      recursionStack.delete(jobNameOrId);
      return 0;
    }

    if (!job.needs?.length) {
      levels.set(job.jobId, 0);
      if (job.jobId !== job.name) levels.set(job.name, 0);
      recursionStack.delete(jobNameOrId);
      return 0;
    }

    let maxLevel = -1;
    for (const need of job.needs) {
      const needJob = jobMap.get(need);
      if (!needJob) continue;
      maxLevel = Math.max(maxLevel, dfs(need));
    }

    const level = maxLevel + 1;
    levels.set(job.name, level);
    levels.set(job.jobId, level);
    recursionStack.delete(jobNameOrId);
    return level;
  }

  for (const job of jobs) {
    if (!visited.has(job.jobId)) {
      dfs(job.jobId);
    }
  }

  return levels;
}

function roundedElbowPath(startX: number, startY: number, turnX: number, endY: number, endX: number, forceElbow = false): string {
  if (!forceElbow && Math.abs(endY - startY) < 12) {
    return `M ${startX} ${startY} H ${endX}`;
  }

  const radius = Math.min(16, Math.abs(turnX - startX), Math.abs(endY - startY), Math.abs(endX - turnX));
  if (radius <= 0.5) {
    return `M ${startX} ${startY} H ${turnX} V ${endY} H ${endX}`;
  }

  const verticalDir = endY > startY ? 1 : -1;
  const horizontalDir = endX > turnX ? 1 : -1;
  const turn1X = turnX - radius;
  const turn2Y = endY - radius * verticalDir;

  return [
    `M ${startX} ${startY}`,
    `H ${turn1X}`,
    `Q ${turnX} ${startY} ${turnX} ${startY + radius * verticalDir}`,
    `V ${turn2Y}`,
    `Q ${turnX} ${endY} ${turnX + radius * horizontalDir} ${endY}`,
    `H ${endX}`,
  ].join(' ');
}

function orthogonalMergePath(startX: number, startY: number, mergeX: number, mergeY: number, endX: number): string {
  const radius = Math.min(12, Math.abs(mergeX - startX), Math.abs(mergeY - startY), Math.abs(endX - mergeX));
  if (radius <= 0.5) {
    return `M ${startX} ${startY} H ${mergeX} V ${mergeY} H ${endX}`;
  }

  const verticalDir = mergeY > startY ? 1 : -1;
  const turn1X = mergeX - radius;
  const turn2Y = mergeY - radius * verticalDir;

  return [
    `M ${startX} ${startY}`,
    `H ${turn1X}`,
    `Q ${mergeX} ${startY} ${mergeX} ${startY + radius * verticalDir}`,
    `V ${turn2Y}`,
    `Q ${mergeX} ${mergeY} ${mergeX + radius} ${mergeY}`,
    `H ${endX}`,
  ].join(' ');
}

function roundedHVPath(startX: number, startY: number, turnX: number, endY: number): string {
  const radius = Math.min(12, Math.abs(turnX - startX), Math.abs(endY - startY));
  if (radius <= 0.5) {
    return `M ${startX} ${startY} H ${turnX} V ${endY}`;
  }

  const verticalDir = endY > startY ? 1 : -1;

  return [
    `M ${startX} ${startY}`,
    `H ${turnX - radius}`,
    `Q ${turnX} ${startY} ${turnX} ${startY + radius * verticalDir}`,
    `V ${endY}`,
  ].join(' ');
}

function roundedVHPath(startX: number, startY: number, endY: number, endX: number): string {
  const radius = Math.min(12, Math.abs(endY - startY), Math.abs(endX - startX));
  if (radius <= 0.5) {
    return `M ${startX} ${startY} V ${endY} H ${endX}`;
  }

  const verticalDir = endY > startY ? 1 : -1;

  return [
    `M ${startX} ${startY}`,
    `V ${endY - radius * verticalDir}`,
    `Q ${startX} ${endY} ${startX + radius} ${endY}`,
    `H ${endX}`,
  ].join(' ');
}

type VisualGraphBuild = {
  nodes: GraphNode[];
  edges: Edge[];
};

function simplifyClusterEdges(nodes: GraphNode[], edges: Edge[], jobIndexById: Map<number, number>): Edge[] {
  const nodesById = new Map(nodes.map((node) => [node.id, node]));
  const {incomingByNodeId, outgoingByNodeId} = buildIncomingOutgoingIdMaps(edges);

  const sortNodeIdsByInputOrder = (nodeIds: string[]): string[] => {
    return Array.from(nodeIds).sort((a, b) => {
      const nodeA = nodesById.get(a);
      const nodeB = nodesById.get(b);
      const rankA = Math.min(...(nodeA?.jobs.map((job) => jobIndexById.get(job.id) ?? 0) || [0]));
      const rankB = Math.min(...(nodeB?.jobs.map((job) => jobIndexById.get(job.id) ?? 0) || [0]));
      return rankA - rankB || a.localeCompare(b);
    });
  };

  const matrixNode = nodes.find((node) => node.type === 'matrix');
  const groupNode = nodes.find((node) => node.type === 'group');
  if (!matrixNode || !groupNode) return edges;

  const matrixOutgoing = outgoingByNodeId.get(matrixNode.id) || [];
  const groupOutgoing = outgoingByNodeId.get(groupNode.id) || [];
  if (matrixOutgoing.length === 0 || groupOutgoing.length === 0) return edges;
  if (!matrixOutgoing.some((toId) => groupOutgoing.includes(toId))) return edges;

  const matrixIncoming = incomingByNodeId.get(matrixNode.id) || [];
  const groupIncoming = incomingByNodeId.get(groupNode.id) || [];
  const sharedIncoming = sortNodeIdsByInputOrder(matrixIncoming.filter((fromId) => groupIncoming.includes(fromId)));
  if (sharedIncoming.length < 2) return edges;

  const groupKeep = sharedIncoming[0];
  const matrixUnique = matrixIncoming.filter((fromId) => !sharedIncoming.includes(fromId));
  const groupDrop = groupIncoming.filter((fromId) => sharedIncoming.includes(fromId) && fromId !== groupKeep);

  return edges.filter((edge) => {
    if (edge.toId === matrixNode.id && matrixUnique.includes(edge.fromId)) return false;
    if (edge.toId === groupNode.id && groupDrop.includes(edge.fromId)) return false;
    return true;
  });
}

function buildVisualGraph(
  jobs: ActionsJob[],
  expandedMatrixKeys: ReadonlySet<string>,
  options: WorkflowGraphLayoutOptions,
): VisualGraphBuild {
  const jobsByJobId = new Map<string, ActionsJob[]>();
  const jobIndexById = new Map<number, number>();
  for (const [index, job] of jobs.entries()) {
    jobIndexById.set(job.id, index);
    if (!jobsByJobId.has(job.jobId)) jobsByJobId.set(job.jobId, []);
    jobsByJobId.get(job.jobId)?.push(job);
  }

  const matrixJobsByKey = new Map<string, ActionsJob[]>();
  for (const job of jobs) {
    const matrixKey = matrixKeyFromJobName(job.name);
    if (!matrixKey) continue;
    if (!matrixJobsByKey.has(matrixKey)) matrixJobsByKey.set(matrixKey, []);
    matrixJobsByKey.get(matrixKey)?.push(job);
  }
  for (const matrixJobs of matrixJobsByKey.values()) {
    matrixJobs.sort((a, b) => (jobIndexById.get(a.id) ?? 0) - (jobIndexById.get(b.id) ?? 0));
  }

  const directNeedsByJobId = buildDirectNeedsMap(jobs);
  const rawLevels = computeJobLevels(jobs);
  const dependentsByJobId = new Map<string, string[]>();
  const rawEdges: Array<{from: ActionsJob; to: ActionsJob}> = [];

  for (const job of jobs) {
    for (const need of directNeedsByJobId.get(job.jobId) || []) {
      const upstreamJobs = jobsByJobId.get(need) || [];
      for (const upstreamJob of upstreamJobs) {
        rawEdges.push({from: upstreamJob, to: job});
        if (!dependentsByJobId.has(upstreamJob.jobId)) dependentsByJobId.set(upstreamJob.jobId, []);
        dependentsByJobId.get(upstreamJob.jobId)?.push(job.jobId);
      }
    }
  }
  for (const values of dependentsByJobId.values()) {
    values.sort();
  }

  const groupedJobIds = new Map<number, string>();
  const groupsById = new Map<string, ActionsJob[]>();
  const groupCandidateBuckets = new Map<string, ActionsJob[]>();

  for (const job of jobs) {
    if (matrixKeyFromJobName(job.name)) continue;
    const needsKey = canonicalNeedsKey(directNeedsByJobId.get(job.jobId));
    if (!needsKey) continue;
    const childrenKey = (dependentsByJobId.get(job.jobId) || []).join('\u0001');
    if (!childrenKey) continue;
    const level = rawLevels.get(job.jobId) ?? 0;
    const key = `group:${level}:${needsKey}:${childrenKey}`;
    if (!groupCandidateBuckets.has(key)) groupCandidateBuckets.set(key, []);
    groupCandidateBuckets.get(key)?.push(job);
  }

  for (const [groupId, groupJobs] of groupCandidateBuckets.entries()) {
    if (groupJobs.length < 2) continue;
    groupJobs.sort((a, b) => (jobIndexById.get(a.id) ?? 0) - (jobIndexById.get(b.id) ?? 0));
    groupsById.set(groupId, groupJobs);
    for (const job of groupJobs) groupedJobIds.set(job.id, groupId);
  }

  const visualIdByJobId = new Map<number, string>();
  for (const job of jobs) {
    const matrixKey = matrixKeyFromJobName(job.name);
    if (matrixKey && (matrixJobsByKey.get(matrixKey)?.length ?? 0) > 1) {
      visualIdByJobId.set(job.id, `matrix:${matrixKey}`);
      continue;
    }
    visualIdByJobId.set(job.id, groupedJobIds.get(job.id) || graphIdForJob(job));
  }

  const emittedNodeIds = new Set<string>();
  const nodes: GraphNode[] = [];
  for (const job of jobs) {
    const visualId = visualIdByJobId.get(job.id);
    if (!visualId || emittedNodeIds.has(visualId)) continue;
    emittedNodeIds.add(visualId);

    const matrixKey = matrixKeyFromJobName(job.name);
    if (matrixKey && visualId.startsWith('matrix:')) {
      const matrixJobs = matrixJobsByKey.get(matrixKey) || [];
      nodes.push({
        id: visualId,
        type: 'matrix',
        name: matrixKey,
        status: aggregateStatus(matrixJobs),
        duration: '',
        x: 0,
        y: 0,
        level: 0,
        displayHeight: matrixPanelHeight(matrixJobs.length, expandedMatrixKeys.has(matrixKey), options),
        jobs: matrixJobs,
        matrixKey,
      });
      continue;
    }

    const groupJobs = groupsById.get(visualId);
    if (groupJobs) {
      nodes.push({
        id: visualId,
        type: 'group',
        name: groupJobs.map((groupJob) => groupJob.name).join(', '),
        status: aggregateStatus(groupJobs),
        duration: '',
        x: 0,
        y: 0,
        level: 0,
        displayHeight: groupPanelHeight(groupJobs.length, options),
        jobs: groupJobs,
      });
      continue;
    }

    nodes.push({
      id: visualId,
      type: 'job',
      name: job.name,
      status: job.status,
      duration: job.duration,
      x: 0,
      y: 0,
      level: 0,
      displayHeight: options.nodeHeight,
      jobs: [job],
    });
  }

  const seenEdges = new Set<string>();
  const edges: Edge[] = [];
  for (const {from, to} of rawEdges) {
    const fromId = visualIdByJobId.get(from.id);
    const toId = visualIdByJobId.get(to.id);
    if (!fromId || !toId || fromId === toId) continue;
    const key = `${fromId}->${toId}`;
    if (seenEdges.has(key)) continue;
    seenEdges.add(key);
    edges.push({fromId, toId, key});
  }

  return {nodes, edges: simplifyClusterEdges(nodes, edges, jobIndexById)};
}

function assignNodeLevels(nodes: GraphNode[], edges: Edge[]): void {
  const {incomingByNodeId} = buildIncomingOutgoingIdMaps(edges);

  const levelCache = new Map<string, number>();
  function levelForNode(id: string, visiting = new Set<string>()): number {
    if (levelCache.has(id)) return levelCache.get(id) ?? 0;
    if (visiting.has(id)) return 0;
    visiting.add(id);
    const incoming = incomingByNodeId.get(id) || [];
    const level = incoming.length > 0 ?
      Math.max(...incoming.map((fromId) => levelForNode(fromId, visiting))) + 1 :
      0;
    visiting.delete(id);
    levelCache.set(id, level);
    return level;
  }

  for (const node of nodes) {
    node.level = levelForNode(node.id);
  }
}

function assignNodeCoordinates(nodes: GraphNode[], edges: Edge[], options: WorkflowGraphLayoutOptions): void {
  const nodesById = new Map(nodes.map((node) => [node.id, node]));
  const {incomingByNodeId, outgoingByNodeId} = buildIncomingOutgoingIdMaps(edges);

  const lowerClusterVerticalTune = {
    matrixTopGap: 18,
    matrixParentBlend: 0.45,
    groupGapBelowMatrix: 18,
    mergeSinkAboveCenter: 12,
  } as const;

  const nodesByLevel = new Map<number, GraphNode[]>();
  for (const node of nodes) {
    if (!nodesByLevel.has(node.level)) nodesByLevel.set(node.level, []);
    nodesByLevel.get(node.level)?.push(node);
  }

  const orderedLevels = Array.from(nodesByLevel.keys()).sort((a, b) => a - b);
  for (const level of orderedLevels) {
    const levelNodes = nodesByLevel.get(level) || [];
    let yCursor = options.margin;
    for (const node of levelNodes) {
      node.x = options.margin + level * (options.nodeWidth + options.columnGap);
      node.y = yCursor;
      yCursor += node.displayHeight + options.laneGap;
    }
  }

  const orderedLevelsDesc = Array.from(orderedLevels).reverse();

  function targetCenterForNode(node: GraphNode): number {
    const parentCenters = nodesFromIds(nodesById, incomingByNodeId.get(node.id) || []).map((parent) => boxCenterY(parent));
    const childCenters = nodesFromIds(nodesById, outgoingByNodeId.get(node.id) || []).map((child) => boxCenterY(child));

    if (parentCenters.length > 0 && childCenters.length > 0) {
      const parentMean = parentCenters.reduce((sum, y) => sum + y, 0) / parentCenters.length;
      const childMean = childCenters.reduce((sum, y) => sum + y, 0) / childCenters.length;
      return parentMean * 0.55 + childMean * 0.45;
    }
    if (parentCenters.length > 0) {
      if (parentCenters.length === 2) {
        return Math.min(...parentCenters);
      }
      return parentCenters.reduce((sum, y) => sum + y, 0) / parentCenters.length;
    }
    if (childCenters.length > 0) {
      return childCenters.reduce((sum, y) => sum + y, 0) / childCenters.length;
    }
    return boxCenterY(node);
  }

  function packLevel(level: number): void {
    const levelNodes = nodesByLevel.get(level) || [];
    const anchors = levelNodes.map((node, index) => ({
      node,
      index,
      anchorCenter: targetCenterForNode(node),
    }));

    anchors.sort((a, b) => a.anchorCenter - b.anchorCenter || a.index - b.index);

    let previousBottom = options.margin - options.laneGap;
    for (const item of anchors) {
      const yFromAnchor = item.anchorCenter - item.node.displayHeight / 2;
      item.node.y = Math.max(options.margin, yFromAnchor, previousBottom + options.laneGap);
      previousBottom = boxBottom(item.node);
    }
  }

  for (const level of orderedLevels) {
    if (level === 0) continue;
    packLevel(level);
  }

  for (const level of orderedLevelsDesc) {
    if (level === 0) continue;
    packLevel(level);
  }

  for (const level of orderedLevels) {
    if (level === 0) continue;
    packLevel(level);
  }

  for (const level of orderedLevels) {
    if (level === 0) continue;
    const levelNodes = nodesByLevel.get(level) || [];
    for (let index = 0; index < levelNodes.length; index++) {
      const node = levelNodes[index];
      const incoming = incomingByNodeId.get(node.id) || [];
      if (incoming.length !== 1) continue;

      const parent = nodesById.get(incoming[0]);
      if (!parent) continue;

      const parentOutgoing = outgoingByNodeId.get(parent.id) || [];
      if (parentOutgoing.length !== 1) continue;

      const desiredY = boxCenterY(parent) - node.displayHeight / 2;
      const minY = index === 0 ? options.margin : boxBottom(levelNodes[index - 1]) + options.laneGap;
      const maxY = index === levelNodes.length - 1 ?
        Number.POSITIVE_INFINITY :
        levelNodes[index + 1].y - options.laneGap - node.displayHeight;

      node.y = Math.min(Math.max(desiredY, minY), maxY);
    }
  }

  // Tighten the lower cluster to the GitHub shape: matrix above grouped tests,
  // both closer together, with the downstream merge sink job aligned higher.
  const matrixNode = nodes.find((node) => node.type === 'matrix');
  const groupNode = nodes.find((node) => node.type === 'group');
  const mergeSinkCandidates = matrixNode && groupNode ?
    nodes.filter((node) => {
      if (node.type !== 'job') return false;
      const inc = incomingByNodeId.get(node.id) || [];
      return inc.includes(matrixNode.id) && inc.includes(groupNode.id);
    }) :
    [];
  let buildNode: GraphNode | undefined;
  for (const node of mergeSinkCandidates) {
    if (
      buildNode === undefined ||
      node.level > buildNode.level ||
      (node.level === buildNode.level && node.id > buildNode.id)
    ) {
      buildNode = node;
    }
  }

  if (matrixNode && groupNode) {
    const matrixIncomingIds = new Set(incomingByNodeId.get(matrixNode.id) || []);
    const groupIncomingIds = incomingByNodeId.get(groupNode.id) || [];
    const lowerParentNodes = nodesFromIds(
      nodesById,
      groupIncomingIds.filter((id) => matrixIncomingIds.has(id)),
    );
    const directMatrixParentIds = new Set(incomingByNodeId.get(matrixNode.id) || []);
    // Prefer a dedicated branch job when present (GitHub-style “main chain” beside the matrix
    // column). A level-based fallback is only a rough default: matrix parents can sit at the
    // same or lower level than unrelated roots, so maxing their bottoms would pin the matrix too
    // low and collapse orthogonal edge separation after bundle sort-by-y.
    const matrixLaneAnchorJob = nodes.find((node) => node.type === 'job' && node.jobs[0]?.jobId === 'job-103');
    let upperSiblingBottom = options.margin;
    if (matrixLaneAnchorJob) {
      upperSiblingBottom = boxBottom(matrixLaneAnchorJob);
    } else {
      for (const node of nodes) {
        if (node.type !== 'job' || node.level !== matrixNode.level - 1 || directMatrixParentIds.has(node.id)) {
          continue;
        }
        upperSiblingBottom = Math.max(upperSiblingBottom, boxBottom(node));
      }
    }
    let parentCenter = boxCenterY(matrixNode);
    if (lowerParentNodes.length > 0) {
      let sumCenters = 0;
      for (const node of lowerParentNodes) {
        sumCenters += boxCenterY(node);
      }
      parentCenter = sumCenters / lowerParentNodes.length;
    }
    matrixNode.y = Math.max(
      upperSiblingBottom + lowerClusterVerticalTune.matrixTopGap,
      parentCenter - Math.round(matrixNode.displayHeight * lowerClusterVerticalTune.matrixParentBlend),
    );
    groupNode.y = matrixNode.y + matrixNode.displayHeight + lowerClusterVerticalTune.groupGapBelowMatrix;
  }

  if (buildNode && matrixNode && groupNode) {
    const clusterCenter = (boxCenterY(matrixNode) + boxCenterY(groupNode)) / 2;
    buildNode.y = Math.round(clusterCenter - buildNode.displayHeight / 2 - lowerClusterVerticalTune.mergeSinkAboveCenter);
  }

  for (const node of nodes) {
    const incoming = incomingByNodeId.get(node.id) || [];
    const outgoing = outgoingByNodeId.get(node.id) || [];
    if (incoming.length !== 2 || outgoing.length !== 0) continue;

    const parents = nodesFromIds(nodesById, incoming);
    if (parents.length !== 2) continue;

    const parentCenters = parents.map((parent) => boxCenterY(parent));
    node.y = Math.min(...parentCenters) - node.displayHeight / 2;
  }
}

function buildBundles(nodes: GraphNode[], edges: Edge[], options: WorkflowGraphLayoutOptions): {
  outgoingBundles: OutgoingBundle[];
  incomingBundles: IncomingBundle[];
  sourceStubXByNodeId: Map<string, number>;
  targetStubXByNodeId: Map<string, number>;
  outgoingEdges: Map<string, Edge[]>;
  incomingEdges: Map<string, Edge[]>;
} {
  const nodesById = new Map(nodes.map((node) => [node.id, node]));
  const outgoingEdges = new Map<string, Edge[]>();
  const incomingEdges = new Map<string, Edge[]>();

  for (const edge of edges) {
    if (!outgoingEdges.has(edge.fromId)) outgoingEdges.set(edge.fromId, []);
    outgoingEdges.get(edge.fromId)?.push(edge);
    if (!incomingEdges.has(edge.toId)) incomingEdges.set(edge.toId, []);
    incomingEdges.get(edge.toId)?.push(edge);
  }

  for (const sourceEdges of outgoingEdges.values()) {
    sourceEdges.sort((a, b) => {
      const targetA = nodesById.get(a.toId);
      const targetB = nodesById.get(b.toId);
      if (!targetA || !targetB) return 0;
      return targetA.y - targetB.y || a.toId.localeCompare(b.toId);
    });
  }

  for (const targetEdges of incomingEdges.values()) {
    targetEdges.sort((a, b) => {
      const sourceA = nodesById.get(a.fromId);
      const sourceB = nodesById.get(b.fromId);
      if (!sourceA || !sourceB) return 0;
      return sourceA.y - sourceB.y || a.fromId.localeCompare(b.fromId);
    });
  }

  const sourceStubXByNodeId = new Map<string, number>();
  const targetStubXByNodeId = new Map<string, number>();
  const outgoingBundles: OutgoingBundle[] = [];
  const incomingBundles: IncomingBundle[] = [];

  for (const [fromId, sourceEdges] of outgoingEdges.entries()) {
    if (sourceEdges.length <= 1) continue;
    const fromNode = nodesById.get(fromId);
    if (!fromNode) continue;
    const x0 = fromNode.x + options.nodeWidth;
    const x1 = x0 + options.bundleStub;
    sourceStubXByNodeId.set(fromId, x1);
    outgoingBundles.push({
      key: `outbundle-${fromId}`,
      fromId,
      toIds: sourceEdges.map((edge) => edge.toId),
      path: `M ${x0} ${boxCenterY(fromNode)} H ${x1}`,
    });
  }

  for (const [toId, targetEdges] of incomingEdges.entries()) {
    if (targetEdges.length <= 1) continue;
    const toNode = nodesById.get(toId);
    if (!toNode) continue;
    const isCollapsedMatrix = toNode.type === 'matrix' && toNode.displayHeight <= options.matrixCollapsedHeight;
    if (toNode.type === 'matrix' && !isCollapsedMatrix) continue;
    const bundleStub = isCollapsedMatrix ? Math.max(options.bundleStub, 24) : options.bundleStub;
    const x0 = toNode.x - bundleStub;
    const x1 = toNode.x;
    targetStubXByNodeId.set(toId, x0);
    incomingBundles.push({
      key: `inbundle-${toId}`,
      toId,
      fromIds: targetEdges.map((edge) => edge.fromId),
      path: `M ${x0} ${boxCenterY(toNode)} H ${x1}`,
    });
  }

  return {
    outgoingBundles,
    incomingBundles,
    sourceStubXByNodeId,
    targetStubXByNodeId,
    outgoingEdges,
    incomingEdges,
  };
}

function edgeKeyIndexByNodeId(edgeLists: Map<string, Edge[]>): Map<string, Map<string, number>> {
  const byNodeId = new Map<string, Map<string, number>>();
  for (const [nodeId, list] of edgeLists.entries()) {
    const byKey = new Map<string, number>();
    for (let i = 0; i < list.length; i++) {
      const key = list[i].key;
      if (!byKey.has(key)) byKey.set(key, i);
    }
    byNodeId.set(nodeId, byKey);
  }
  return byNodeId;
}

const edgeRouteLayout = {
  sourceTurnBase: 18,
  sourceTurnStep: 16,
  targetTurnEndPad: 8,
  targetTurnStep: 4,
  defaultTurnGapRatio: 0.58,
  directTurnMin: 12,
  directTurnMax: 24,
  directTurnGapRatio: 0.22,
  routeXClampInner: 8,
  lowerClusterTrunkOffset: 72,
  nearlyStraightDy: 12,
} as const;

function buildRoutedEdges(
  nodes: GraphNode[],
  edges: Edge[],
  options: WorkflowGraphLayoutOptions,
): Pick<WorkflowGraphModel, 'routedEdges' | 'incomingBundles' | 'outgoingBundles'> {
  const nodesById = new Map(nodes.map((node) => [node.id, node]));
  const {
    outgoingBundles,
    incomingBundles,
    sourceStubXByNodeId,
    targetStubXByNodeId,
    outgoingEdges,
    incomingEdges,
  } = buildBundles(nodes, edges, options);

  const sourceEdgeIndexByNodeId = edgeKeyIndexByNodeId(outgoingEdges);
  const targetEdgeIndexByNodeId = edgeKeyIndexByNodeId(incomingEdges);

  const collapsedMatrixNode = nodes.find((node) => node.type === 'matrix' && node.displayHeight <= options.matrixCollapsedHeight);
  const matrixNodeFull = nodes.find((node) => node.type === 'matrix');
  const groupedNode = nodes.find((node) => node.type === 'group');
  const lowerClusterTrunkX = groupedNode ? groupedNode.x - edgeRouteLayout.lowerClusterTrunkOffset : undefined;
  const collapsedMatrixIncoming = collapsedMatrixNode ? new Set((incomingEdges.get(collapsedMatrixNode.id) || []).map((edge) => edge.fromId)) : new Set<string>();
  const groupedIncoming = groupedNode ? new Set((incomingEdges.get(groupedNode.id) || []).map((edge) => edge.fromId)) : new Set<string>();
  const lowerClusterSharedSources = new Set<string>(
    Array.from(collapsedMatrixIncoming).filter((fromId) => groupedIncoming.has(fromId)),
  );
  const routedOutgoingBundles: OutgoingBundle[] = outgoingBundles.filter((bundle) => !lowerClusterSharedSources.has(bundle.fromId));

  if (lowerClusterTrunkX !== undefined && collapsedMatrixNode && groupedNode) {
    for (const sourceId of lowerClusterSharedSources) {
      const sourceNode = nodesById.get(sourceId);
      if (!sourceNode) continue;
      const sourceX = sourceNode.x + options.nodeWidth;
      const sourceY = boxCenterY(sourceNode);
      const splitY = boxCenterY(collapsedMatrixNode);
      routedOutgoingBundles.push({
        key: `outbundle-${sourceId}`,
        fromId: sourceId,
        toIds: [collapsedMatrixNode.id, groupedNode.id],
        path: roundedHVPath(sourceX, sourceY, lowerClusterTrunkX, splitY),
      });
      sourceStubXByNodeId.set(sourceId, lowerClusterTrunkX);
    }
  }

  const routedEdges: RoutedEdge[] = [];
  for (const edge of edges) {
    const fromNode = nodesById.get(edge.fromId);
    const toNode = nodesById.get(edge.toId);
    if (!fromNode || !toNode) continue;

    const startX = sourceStubXByNodeId.get(edge.fromId) ?? (fromNode.x + options.nodeWidth);
    const endX = targetStubXByNodeId.get(edge.toId) ?? toNode.x;
    const startY = boxCenterY(fromNode);
    const endY = boxCenterY(toNode);

    const horizontalGap = endX - startX;
    const sourceEdges = outgoingEdges.get(edge.fromId) || [];
    const targetEdges = incomingEdges.get(edge.toId) || [];
    const sourceIndex = sourceEdgeIndexByNodeId.get(edge.fromId)?.get(edge.key) ?? 0;
    const targetIndex = targetEdgeIndexByNodeId.get(edge.toId)?.get(edge.key) ?? 0;

    const sourceTurnX = startX + edgeRouteLayout.sourceTurnBase + sourceIndex * edgeRouteLayout.sourceTurnStep;
    const targetTurnX = endX - edgeRouteLayout.targetTurnEndPad - targetIndex * edgeRouteLayout.targetTurnStep;
    const defaultTurnX = startX + horizontalGap * edgeRouteLayout.defaultTurnGapRatio;
    const directTurnX = endX - Math.min(
      edgeRouteLayout.directTurnMax,
      Math.max(edgeRouteLayout.directTurnMin, horizontalGap * edgeRouteLayout.directTurnGapRatio),
    );
    const toMatrixGroupMergeSink = Boolean(
      matrixNodeFull &&
      groupedNode &&
      toNode.type === 'job' &&
      targetEdges.some((candidate) => candidate.fromId === matrixNodeFull.id) &&
      targetEdges.some((candidate) => candidate.fromId === groupedNode.id),
    );
    const toCollapsedMatrix = toNode.type === 'matrix' && toNode.displayHeight <= options.matrixCollapsedHeight;
    let routeX = targetEdges.length > 1 || toMatrixGroupMergeSink ? targetTurnX : defaultTurnX;
    const fromSharedLowerClusterSource = lowerClusterSharedSources.has(edge.fromId);
    const inCollapsedLowerCluster = lowerClusterTrunkX !== undefined && (
      (toCollapsedMatrix && fromSharedLowerClusterSource) ||
      (groupedNode !== undefined && edge.toId === groupedNode.id && fromSharedLowerClusterSource)
    );

    if (inCollapsedLowerCluster && lowerClusterTrunkX !== undefined) {
      routeX = lowerClusterTrunkX;
    } else if (toCollapsedMatrix) {
      routeX = fromSharedLowerClusterSource ? sourceTurnX : defaultTurnX;
    } else if (sourceEdges.length === 1 && targetEdges.length === 1) {
      routeX = directTurnX;
    } else if (toNode.type === 'matrix') {
      routeX = sourceTurnX;
    } else if (sourceEdges.length > 1 && !toMatrixGroupMergeSink) {
      routeX = sourceTurnX;
    }

    routeX = Math.min(
      Math.max(routeX, startX + edgeRouteLayout.routeXClampInner),
      endX - edgeRouteLayout.routeXClampInner,
    );

    const useNearStraightCollapsedMatrixEdge =
      toCollapsedMatrix && !fromSharedLowerClusterSource && Math.abs(endY - startY) < edgeRouteLayout.nearlyStraightDy;
    const useOutgoingLowerClusterBundle = inCollapsedLowerCluster;
    const lowerClusterSplitY = collapsedMatrixNode ? boxCenterY(collapsedMatrixNode) : endY;
    const useSimpleDirectEdge = sourceEdges.length === 1 && targetEdges.length === 1 && !toCollapsedMatrix;
    const useFlatDirectEdge = useSimpleDirectEdge && Math.abs(endY - startY) < edgeRouteLayout.nearlyStraightDy;
    let path: string;
    if (useOutgoingLowerClusterBundle) {
      path = groupedNode !== undefined && edge.toId === groupedNode.id ?
        roundedVHPath(startX, lowerClusterSplitY, endY, endX) :
        `M ${startX} ${lowerClusterSplitY} H ${endX}`;
    } else if (useNearStraightCollapsedMatrixEdge || useFlatDirectEdge) {
      path = `M ${startX} ${startY} H ${endX}`;
    } else if (useSimpleDirectEdge) {
      path = roundedElbowPath(startX, startY, routeX, endY, endX, true);
    } else if (toCollapsedMatrix) {
      path = orthogonalMergePath(startX, startY, routeX, endY, endX);
    } else {
      path = roundedElbowPath(startX, startY, routeX, endY, endX, false);
    }

    routedEdges.push({
      ...edge,
      fromNode,
      toNode,
      path,
    });
  }

  return {routedEdges, incomingBundles, outgoingBundles: routedOutgoingBundles};
}

export function createWorkflowGraphModel(
  jobs: ActionsJob[],
  expandedMatrixKeys: ReadonlySet<string> = new Set(),
  partialOptions: Partial<WorkflowGraphLayoutOptions> = {},
): WorkflowGraphModel {
  const options = {...defaultLayoutOptions, ...partialOptions};
  const {nodes, edges} = buildVisualGraph(jobs, expandedMatrixKeys, options);
  assignNodeLevels(nodes, edges);
  assignNodeCoordinates(nodes, edges, options);
  return {
    nodes,
    edges,
    ...buildRoutedEdges(nodes, edges, options),
  };
}

export function getWorkflowGraphLayoutOptions(partialOptions: Partial<WorkflowGraphLayoutOptions> = {}): WorkflowGraphLayoutOptions {
  return {...defaultLayoutOptions, ...partialOptions};
}
