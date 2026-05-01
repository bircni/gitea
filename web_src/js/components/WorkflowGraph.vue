<script setup lang="ts">
import {computed, onMounted, onUnmounted, ref, watch} from 'vue';
import {SvgIcon} from '../svg.ts';
import ActionRunStatus from './ActionRunStatus.vue';
import {localUserSettings} from '../modules/user-settings.ts';
import {isPlainClick} from '../utils/dom.ts';
import {debounce} from 'throttle-debounce';
import type {ActionsJob, ActionsRunStatus} from '../modules/gitea-actions.ts';
import type {ActionRunViewStore} from './ActionRunView.ts';

type GraphNodeType = 'job' | 'matrix' | 'group';

interface GraphNode {
  id: string;
  type: GraphNodeType;
  name: string;
  status: ActionsRunStatus;
  duration: string;

  x: number;
  y: number;
  level: number;

  /** Pixel height of the rendered visual node. */
  displayHeight: number;
  jobs: ActionsJob[];
  matrixKey?: string;
}

interface Edge {
  fromId: string;
  toId: string;
  key: string;
}

interface RoutedEdge extends Edge {
  path: string;
  fromNode: GraphNode;
  toNode: GraphNode;
}

interface StoredState {
  scale: number;
  translateX: number;
  translateY: number;
  timestamp: number;
}

const props = defineProps<{
  store: ActionRunViewStore;
  jobs: ActionsJob[];
  runLink: string;
  workflowId: string;
}>()

const settingKeyStates = 'actions-graph-states';
const maxStoredStates = 10;

const scale = ref(1);
const translateX = ref(0);
const translateY = ref(0);
const isDragging = ref(false);
const lastMousePos = ref({x: 0, y: 0});
const graphContainer = ref<HTMLElement | null>(null);
const hoveredGraphId = ref<string | null>(null);

const nodeHeight = 52;
const verticalSpacing = 90;
const margin = 40;
const interCardGap = verticalSpacing - nodeHeight;
const groupPanelHeaderHeight = 0;
const groupPanelRowHeight = 46;
const groupPanelPadY = 14;
const matrixCollapsedHeight = 104;
const matrixPanelHeaderH = 32;
const matrixPanelRowH = 38;
const matrixPanelPadY = 14;

const stateKey = () => `${props.store.viewData.currentRun.repoId}-${props.workflowId}`;

const expandedMatrixKeys = ref<Set<string>>(new Set());

function isMatrixExpanded(key: string): boolean {
  return expandedMatrixKeys.value.has(key);
}

function toggleMatrixExpanded(key: string) {
  const next = new Set(expandedMatrixKeys.value);
  if (next.has(key)) next.delete(key);
  else next.add(key);
  expandedMatrixKeys.value = next;
}

function matrixKeyFromJobName(name: string): string | null {
  // Heuristic: GitHub matrix jobs are commonly rendered like: "matrix-job (dims...)"
  const idx = name.indexOf(' (');
  if (idx === -1) return null;
  return name.slice(0, idx).trim() || null;
}

function boxBottom(job: GraphNode): number {
  return job.y + job.displayHeight;
}

function boxCenterY(job: GraphNode): number {
  return job.y + job.displayHeight / 2;
}

function matrixPanelHeight(rowCount: number, expanded: boolean): number {
  if (rowCount <= 0) return nodeHeight;
  if (!expanded) return matrixCollapsedHeight;
  return matrixPanelHeaderH + rowCount * matrixPanelRowH + matrixPanelPadY;
}

function groupPanelHeight(rowCount: number): number {
  return groupPanelHeaderHeight + rowCount * groupPanelRowHeight + groupPanelPadY * 2;
}

function compareStatusWorstFirst(a: ActionsRunStatus, b: ActionsRunStatus): number {
  const rank = (s: ActionsRunStatus) => {
    if (s === 'failure') return 0;
    if (s === 'cancelled') return 1;
    if (s === 'running') return 2;
    if (s === 'waiting') return 3;
    if (s === 'success') return 4;
    return 5;
  };
  return rank(a) - rank(b);
}

function aggregateMatrixStatus(children: ActionsJob[]): ActionsRunStatus {
  return children.map((c) => c.status).slice().sort(compareStatusWorstFirst)[0]!;
}

const loadSavedState = () => {
  const allStates = localUserSettings.getJsonObject<Record<string, StoredState>>(settingKeyStates, {});
  const saved = allStates[stateKey()];
  if (!saved) return;
  scale.value = clampScale(saved.scale ?? scale.value);
  translateX.value = saved.translateX ?? translateX.value;
  translateY.value = saved.translateY ?? translateY.value;
};

const saveState = () => {
  const allStates = localUserSettings.getJsonObject<Record<string, StoredState>>(settingKeyStates, {});
  allStates[stateKey()] = {
    scale: scale.value,
    translateX: translateX.value,
    translateY: translateY.value,
    timestamp: Date.now(),
  };

  const sortedStates = Object.entries(allStates)
    .sort(([, a], [, b]) => b.timestamp - a.timestamp)
    .slice(0, maxStoredStates);

  localUserSettings.setJsonObject(settingKeyStates, Object.fromEntries(sortedStates));
};

const minNodeWidth = 168;
const maxNodeWidth = 232;
const nodeWidth = computed(() => {
  const maxNameLength = Math.max(...props.jobs.map(j => j.name.length), 0);
  return Math.min(Math.max(minNodeWidth, maxNameLength * 8), maxNodeWidth);
});

const horizontalSpacing = computed(() => nodeWidth.value + 84);
const graphWidth = computed(() => {
  if (jobsWithLayout.value.length === 0) return 800;
  const maxX = Math.max(...jobsWithLayout.value.map(j => j.x + nodeWidth.value));
  return maxX + margin * 2;
});

const graphHeight = computed(() => {
  if (jobsWithLayout.value.length === 0) return 400;
  const maxY = Math.max(...jobsWithLayout.value.map(j => boxBottom(j)));
  return maxY + margin * 2;
});


function canonicalNeedsKey(needs: string[] | undefined): string {
  if (!needs?.length) return '';
  return [...needs].sort().join('\u0001');
}

function graphIdForJob(job: ActionsJob): string {
  return `job:${job.id}`;
}

function buildDirectNeedsMap(jobs: ActionsJob[]): Map<string, string[]> {
  const directNeedsByJobId = new Map<string, string[]>();
  const dependentsByJobId = new Map<string, Set<string>>();

  for (const job of jobs) {
    const needs = job.needs || [];
    directNeedsByJobId.set(job.jobId, needs);

    for (const need of needs) {
      if (!dependentsByJobId.has(need)) {
        dependentsByJobId.set(need, new Set());
      }
      dependentsByJobId.get(need)!.add(job.jobId);
    }
  }

  const reachabilityCache = new Map<string, boolean>();

  function canReach(fromJobId: string, toJobId: string): boolean {
    const cacheKey = `${fromJobId}->${toJobId}`;
    if (reachabilityCache.has(cacheKey)) {
      return reachabilityCache.get(cacheKey)!;
    }

    const visited = new Set<string>();
    const stack = [...(dependentsByJobId.get(fromJobId) || [])];

    while (stack.length > 0) {
      const current = stack.pop()!;
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

const directNeedsByJobId = computed(() => buildDirectNeedsMap(props.jobs));

const visualGraph = computed(() => {
  const jobsByJobId = new Map<string, ActionsJob[]>();
  const jobIndexById = new Map<number, number>();
  props.jobs.forEach((job, index) => {
    jobIndexById.set(job.id, index);
    if (!jobsByJobId.has(job.jobId)) {
      jobsByJobId.set(job.jobId, []);
    }
    jobsByJobId.get(job.jobId)!.push(job);
  });

  const matrixJobsByKey = new Map<string, ActionsJob[]>();
  for (const job of props.jobs) {
    const matrixKey = matrixKeyFromJobName(job.name);
    if (!matrixKey) continue;
    if (!matrixJobsByKey.has(matrixKey)) matrixJobsByKey.set(matrixKey, []);
    matrixJobsByKey.get(matrixKey)!.push(job);
  }
  for (const [, jobs] of matrixJobsByKey) {
    jobs.sort((a, b) => (jobIndexById.get(a.id) ?? 0) - (jobIndexById.get(b.id) ?? 0));
  }

  const rawEdges: Array<{from: ActionsJob; to: ActionsJob}> = [];
  const dependentsByJobId = new Map<string, string[]>();
  for (const job of props.jobs) {
    for (const need of directNeedsByJobId.value.get(job.jobId) || []) {
      const upstreamJobs = jobsByJobId.get(need) || [];
      for (const upstreamJob of upstreamJobs) {
        rawEdges.push({from: upstreamJob, to: job});
        if (!dependentsByJobId.has(upstreamJob.jobId)) dependentsByJobId.set(upstreamJob.jobId, []);
        dependentsByJobId.get(upstreamJob.jobId)!.push(job.jobId);
      }
    }
  }
  for (const [, values] of dependentsByJobId.entries()) {
    values.sort();
  }

  const rawLevels = computeJobLevels(props.jobs);
  const groupedJobIds = new Map<number, string>();
  const groupsById = new Map<string, ActionsJob[]>();
  const groupCandidateBuckets = new Map<string, ActionsJob[]>();

  for (const job of props.jobs) {
    if (matrixKeyFromJobName(job.name)) continue;
    const needsKey = canonicalNeedsKey(directNeedsByJobId.value.get(job.jobId));
    if (!needsKey) continue;
    const childrenKey = (dependentsByJobId.get(job.jobId) || []).join('\u0001');
    if (!childrenKey) continue;
    const level = rawLevels.get(job.jobId) || rawLevels.get(job.name) || 0;
    const key = `group:${level}:${needsKey}:${childrenKey}`;
    if (!groupCandidateBuckets.has(key)) groupCandidateBuckets.set(key, []);
    groupCandidateBuckets.get(key)!.push(job);
  }

  for (const [groupId, jobs] of groupCandidateBuckets.entries()) {
    if (jobs.length < 2) continue;
    jobs.sort((a, b) => (jobIndexById.get(a.id) ?? 0) - (jobIndexById.get(b.id) ?? 0));
    groupsById.set(groupId, jobs);
    for (const job of jobs) {
      groupedJobIds.set(job.id, groupId);
    }
  }

  const visualIdByJobId = new Map<number, string>();
  for (const job of props.jobs) {
    const matrixKey = matrixKeyFromJobName(job.name);
    if (matrixKey && (matrixJobsByKey.get(matrixKey)?.length || 0) > 1) {
      visualIdByJobId.set(job.id, `matrix:${matrixKey}`);
      continue;
    }
    const groupId = groupedJobIds.get(job.id);
    visualIdByJobId.set(job.id, groupId || graphIdForJob(job));
  }

  const emittedNodeIds = new Set<string>();
  const nodes: GraphNode[] = [];
  for (const job of props.jobs) {
    const visualId = visualIdByJobId.get(job.id)!;
    if (emittedNodeIds.has(visualId)) continue;
    emittedNodeIds.add(visualId);

    const matrixKey = matrixKeyFromJobName(job.name);
    if (matrixKey && visualId.startsWith('matrix:')) {
      const jobs = matrixJobsByKey.get(matrixKey)!;
      nodes.push({
        id: visualId,
        type: 'matrix',
        name: matrixKey,
        status: aggregateMatrixStatus(jobs),
        duration: '',
        x: 0,
        y: 0,
        level: 0,
        displayHeight: matrixPanelHeight(jobs.length, isMatrixExpanded(matrixKey)),
        jobs,
        matrixKey,
      });
      continue;
    }

    const groupJobs = groupsById.get(visualId);
    if (groupJobs) {
      nodes.push({
        id: visualId,
        type: 'group',
        name: groupJobs.map((j) => j.name).join(', '),
        status: aggregateMatrixStatus(groupJobs),
        duration: '',
        x: 0,
        y: 0,
        level: 0,
        displayHeight: groupPanelHeight(groupJobs.length),
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
      displayHeight: nodeHeight,
      jobs: [job],
    });
  }

  const edgesList: Edge[] = [];
  const seenEdges = new Set<string>();
  for (const {from, to} of rawEdges) {
    const fromId = visualIdByJobId.get(from.id)!;
    const toId = visualIdByJobId.get(to.id)!;
    if (fromId === toId) continue;
    const key = `${fromId}->${toId}`;
    if (seenEdges.has(key)) continue;
    seenEdges.add(key);
    edgesList.push({fromId, toId, key});
  }

  const incomingByNodeId = new Map<string, string[]>();
  for (const edge of edgesList) {
    if (!incomingByNodeId.has(edge.toId)) incomingByNodeId.set(edge.toId, []);
    incomingByNodeId.get(edge.toId)!.push(edge.fromId);
  }

  const levelCache = new Map<string, number>();
  function levelForNode(id: string, visiting = new Set<string>()): number {
    if (levelCache.has(id)) return levelCache.get(id)!;
    if (visiting.has(id)) return 0;
    visiting.add(id);
    const incoming = incomingByNodeId.get(id) || [];
    const level = incoming.length ? Math.max(...incoming.map((fromId) => levelForNode(fromId, visiting))) + 1 : 0;
    visiting.delete(id);
    levelCache.set(id, level);
    return level;
  }

  for (const node of nodes) {
    node.level = levelForNode(node.id);
  }

  const nodesByLevel = new Map<number, GraphNode[]>();
  for (const node of nodes) {
    if (!nodesByLevel.has(node.level)) nodesByLevel.set(node.level, []);
    nodesByLevel.get(node.level)!.push(node);
  }

  const columnBottoms = new Map<number, number>();
  for (const [level, levelNodes] of nodesByLevel.entries()) {
    let yCursor = margin;
    for (let i = 0; i < levelNodes.length; i++) {
      const node = levelNodes[i]!;
      node.x = margin + level * horizontalSpacing.value;
      node.y = yCursor;
      yCursor += node.displayHeight + (i < levelNodes.length - 1 ? interCardGap : 0);
    }
    columnBottoms.set(level, yCursor);
  }

  const maxBottom = Math.max(...columnBottoms.values(), margin);
  for (const [level, levelNodes] of nodesByLevel.entries()) {
    const yShift = (maxBottom - (columnBottoms.get(level) || margin)) / 2;
    for (const node of levelNodes) {
      node.y += yShift;
    }
  }

  return {nodes, edges: edgesList};
});

const jobsWithLayout = computed(() => visualGraph.value.nodes);

const edges = computed(() => visualGraph.value.edges);

function buildGithubLikeConnectorPath(startX: number, startY: number, endX: number, endY: number, turnX: number): string {
  // GitHub-like: smooth S-curves that still exit/enter nodes horizontally.
  if (Math.abs(endY - startY) < 1) {
    return `M ${startX} ${startY} H ${endX}`;
  }

  const minX = Math.min(startX, endX);
  const maxX = Math.max(startX, endX);
  const mx = Math.min(Math.max(turnX, minX + 12), maxX - 12);

  const dx = Math.abs(endX - startX);
  const dy = Math.abs(endY - startY);
  if (dx < 8) {
    // Too tight for a meaningful S-curve; fall back to a simple elbow.
    const midY = (startY + endY) / 2;
    return `M ${startX} ${startY} V ${midY} H ${endX} V ${endY}`;
  }
  const h = Math.max(18, Math.min(44, dx * 0.35, dy * 0.55));

  const c1x = startX + (mx - startX) * 0.55;
  const c2x = endX - (endX - mx) * 0.55;

  return [
    `M ${startX} ${startY}`,
    `C ${c1x} ${startY} ${mx} ${startY + Math.sign(endY - startY) * h} ${mx} ${(startY + endY) / 2}`,
    `C ${mx} ${endY - Math.sign(endY - startY) * h} ${c2x} ${endY} ${endX} ${endY}`,
  ].join(' ');
}

const routedEdges = computed<RoutedEdge[]>(() => {
  const nodesById = new Map(jobsWithLayout.value.map((job) => [job.id, job]));
  const outgoingEdges = new Map<string, Edge[]>();
  const incomingEdges = new Map<string, Edge[]>();

  for (const edge of edges.value) {
    if (!outgoingEdges.has(edge.fromId)) {
      outgoingEdges.set(edge.fromId, []);
    }
    outgoingEdges.get(edge.fromId)!.push(edge);

    if (!incomingEdges.has(edge.toId)) {
      incomingEdges.set(edge.toId, []);
    }
    incomingEdges.get(edge.toId)!.push(edge);
  }

  for (const sourceEdges of outgoingEdges.values()) {
    sourceEdges.sort((a, b) => {
      const targetA = nodesById.get(a.toId);
      const targetB = nodesById.get(b.toId);
      if (!targetA || !targetB) return 0;
      return targetA.y - targetB.y || a.toId.localeCompare(b.toId);
    });
  }

  // Bundle incoming edges: if a node has multiple parents, draw one shared
  // short "trunk" into the node and route each edge into the trunk point.
  const bundleXByTargetId = new Map<string, number>();
  for (const [toId, inc] of incomingEdges.entries()) {
    if (inc.length <= 1) continue;
    const toNode = nodesById.get(toId);
    if (!toNode) continue;
    bundleXByTargetId.set(toId, toNode.x - 18);
  }

  // Bundle outgoing edges: if a node has multiple children, route each edge from a
  // shared "trunk" point leaving the node.
  const bundleXBySourceId = new Map<string, number>();
  for (const [fromId, out] of outgoingEdges.entries()) {
    if (out.length <= 1) continue;
    const fromNode = nodesById.get(fromId);
    if (!fromNode) continue;
    bundleXBySourceId.set(fromId, fromNode.x + nodeWidth.value + 18);
  }

  const edgePaths: RoutedEdge[] = [];

  for (const edge of edges.value) {
    const fromNode = nodesById.get(edge.fromId);
    const toNode = nodesById.get(edge.toId);
    if (!fromNode || !toNode) continue;

    const startX = bundleXBySourceId.get(edge.fromId) ?? (fromNode.x + nodeWidth.value);
    const startY = boxCenterY(fromNode);
    const endX = bundleXByTargetId.get(edge.toId) ?? toNode.x;
    const endY = boxCenterY(toNode);
    const sourceEdges = outgoingEdges.get(edge.fromId) || [];
    const targetEdges = incomingEdges.get(edge.toId) || [];
    const horizontalGap = endX - startX;
    const turnOffset = Math.min(28, Math.max(16, horizontalGap * 0.14));
    const sourceTurnX = startX + turnOffset;
    const targetTurnX = endX - turnOffset;

    let turnX = startX + horizontalGap / 2;
    // Spread parallel edges so they diverge earlier/later instead of stacking and creating
    // unnecessary crossings/ambiguity.
    const sourceIndex = sourceEdges.findIndex((e) => e.key === edge.key);
    const targetIndex = targetEdges.findIndex((e) => e.key === edge.key);
    const spread = 7;
    if (sourceEdges.length > 1) {
      turnX = sourceTurnX + Math.max(0, sourceIndex) * spread;
    } else if (targetEdges.length > 1) {
      turnX = targetTurnX - Math.max(0, targetIndex) * spread;
    }
    // Avoid over-shooting which can create strange loops for very tight layouts.
    turnX = Math.min(Math.max(turnX, startX + 10), endX - 10);

    const path = buildGithubLikeConnectorPath(startX, startY, endX, endY, turnX);

    edgePaths.push({
      ...edge,
      path,
      fromNode,
      toNode,
    });
  }

  return edgePaths;
});

type IncomingBundle = {
  key: string;
  toId: string;
  fromIds: string[];
  path: string;
};

type OutgoingBundle = {
  key: string;
  fromId: string;
  toIds: string[];
  path: string;
};

const incomingBundles = computed<IncomingBundle[]>(() => {
  const nodesById = new Map(jobsWithLayout.value.map((job) => [job.id, job]));

  const fromIdsByTarget = new Map<string, string[]>();
  for (const e of edges.value) {
    if (!fromIdsByTarget.has(e.toId)) fromIdsByTarget.set(e.toId, []);
    fromIdsByTarget.get(e.toId)!.push(e.fromId);
  }

  const bundles: IncomingBundle[] = [];
  for (const [toId, fromIds] of fromIdsByTarget.entries()) {
    if (fromIds.length <= 1) continue;
    const toNode = nodesById.get(toId);
    if (!toNode) continue;
    const x0 = toNode.x - 18;
    const x1 = toNode.x;
    const y = boxCenterY(toNode);
    bundles.push({
      key: `inbundle-${toId}`,
      toId,
      fromIds,
      path: `M ${x0} ${y} H ${x1}`,
    });
  }
  return bundles;
});

const outgoingBundles = computed<OutgoingBundle[]>(() => {
  const nodesById = new Map(jobsWithLayout.value.map((job) => [job.id, job]));

  const toIdsBySource = new Map<string, string[]>();
  for (const e of edges.value) {
    if (!toIdsBySource.has(e.fromId)) toIdsBySource.set(e.fromId, []);
    toIdsBySource.get(e.fromId)!.push(e.toId);
  }

  const bundles: OutgoingBundle[] = [];
  for (const [fromId, toIds] of toIdsBySource.entries()) {
    if (toIds.length <= 1) continue;
    const fromNode = nodesById.get(fromId);
    if (!fromNode) continue;
    const x0 = fromNode.x + nodeWidth.value;
    const x1 = x0 + 18;
    const y = boxCenterY(fromNode);
    bundles.push({
      key: `outbundle-${fromId}`,
      fromId,
      toIds,
      path: `M ${x0} ${y} H ${x1}`,
    });
  }
  return bundles;
});

const graphMetrics = computed(() => {
  const successCount = props.jobs.filter(job => job.status === 'success').length;

  const levels = new Map<number, number>();
  jobsWithLayout.value.forEach(job => {
    const count = levels.get(job.level) || 0;
    levels.set(job.level, count + 1);
  })
  const parallelism = Math.max(...Array.from(levels.values()), 0);

  return {
    successRate: `${((successCount / props.jobs.length) * 100).toFixed(0)}%`,
    parallelism,
  };
})

const minScale = 0.3;
const maxScale = 1;

function clampScale(nextScale: number): number {
  return Math.min(Math.max(Math.round(nextScale * 100) / 100, minScale), maxScale);
}

const canZoomIn = computed(() => scale.value < maxScale);

function zoomTo(nextScale: number) {
  scale.value = clampScale(nextScale);
}

function zoomIn() {
  zoomTo(scale.value * 1.2);
}

function zoomOut() {
  zoomTo(scale.value / 1.2);
}

function resetView() {
  scale.value = 1;
  translateX.value = 0;
  translateY.value = 0;
}

function handleMouseDown(e: MouseEvent) {
  if (!isPlainClick(e)) return;

  // don't start drag on interactive/text elements inside the SVG
  const target = e.target as Element;
  const interactive = target.closest('div, p, a, span, button, input, text, .job-node-group');
  if (interactive?.closest('svg')) return;

  e.preventDefault();

  isDragging.value = true;
  lastMousePos.value = {x: e.clientX, y: e.clientY};
  graphContainer.value!.style.cursor = 'grabbing';
}

function handleMouseMoveOnDocument(event: MouseEvent) {
  if (!isDragging.value) return;

  const dx = event.clientX - lastMousePos.value.x;
  const dy = event.clientY - lastMousePos.value.y;

  translateX.value += dx;
  translateY.value += dy;

  lastMousePos.value = {x: event.clientX, y: event.clientY};
}

function handleMouseUpOnDocument() {
  if (!isDragging.value) return;
  isDragging.value = false;
  graphContainer.value!.style.cursor = 'grab';
}

function handleWheel(event: WheelEvent) {
  // Without a modifier, let the wheel scroll the page
  if (!event.ctrlKey && !event.metaKey) {
    return;
  }
  event.preventDefault();
  const zoomFactor = Math.exp(-event.deltaY * 0.0015);
  zoomTo(scale.value * zoomFactor);
}

onMounted(() => {
  loadSavedState();
  watch([translateX, translateY, scale], debounce(500, saveState));
  watch([scale], debounce(100, saveState));

  document.addEventListener('mousemove', handleMouseMoveOnDocument);
  document.addEventListener('mouseup', handleMouseUpOnDocument);
});

onUnmounted(() => {
  document.removeEventListener('mousemove', handleMouseMoveOnDocument);
  document.removeEventListener('mouseup', handleMouseUpOnDocument);
});

function handleNodeMouseEnter(job: GraphNode) {
  hoveredGraphId.value = job.id;
}

function handleNodeMouseLeave() {
  hoveredGraphId.value = null;
}

function handleMatrixMouseEnter(groupId: string) {
  hoveredGraphId.value = groupId;
}

function isEdgeHighlighted(edge: RoutedEdge): boolean {
  if (!hoveredGraphId.value) {
    return false;
  }
  return edge.fromId === hoveredGraphId.value || edge.toId === hoveredGraphId.value;
}

function isIncomingBundleHighlighted(bundle: IncomingBundle): boolean {
  if (!hoveredGraphId.value) return false;
  return bundle.toId === hoveredGraphId.value || bundle.fromIds.includes(hoveredGraphId.value);
}

function isOutgoingBundleHighlighted(bundle: OutgoingBundle): boolean {
  if (!hoveredGraphId.value) return false;
  return bundle.fromId === hoveredGraphId.value || bundle.toIds.includes(hoveredGraphId.value);
}

const nodesWithIncomingEdge = computed(() => {
  const set = new Set<string>();
  for (const edge of routedEdges.value) set.add(edge.toId);
  return set;
});

const nodesWithOutgoingEdge = computed(() => {
  const set = new Set<string>();
  for (const edge of routedEdges.value) set.add(edge.fromId);
  return set;
});


function computeJobLevels(jobs: ActionsJob[]): Map<string, number> {
  const jobMap = new Map<string, ActionsJob>()
  jobs.forEach(job => {
    jobMap.set(job.name, job);
    if (job.jobId) jobMap.set(job.jobId, job);
  });

  const levels = new Map<string, number>();
  const visited = new Set<string>();
  const recursionStack = new Set<string>();
  const MAX_DEPTH = 100;

  function dfs(jobNameOrId: string, depth: number = 0): number {
    if (depth > MAX_DEPTH) {
      console.error(`Max recursion depth (${MAX_DEPTH}) reached for: ${jobNameOrId}`);
      return 0;
    }

    if (recursionStack.has(jobNameOrId)) {
      console.error(`Cycle detected involving: ${jobNameOrId}`);
      return 0;
    }

    if (visited.has(jobNameOrId)) {
      return levels.get(jobNameOrId) || 0;
    }

    recursionStack.add(jobNameOrId);
    visited.add(jobNameOrId);

    const job = jobMap.get(jobNameOrId);
    if (!job) {
      recursionStack.delete(jobNameOrId);
      return 0;
    }

    if (!job.needs?.length) {
      levels.set(job.jobId, 0);
      recursionStack.delete(jobNameOrId);
      return 0;
    }

    let maxLevel = -1;
    for (const need of job.needs) {
      const needJob = jobMap.get(need);
      if (!needJob) continue;

      const needLevel = dfs(need, depth + 1);
      maxLevel = Math.max(maxLevel, needLevel);
    }

    const level = maxLevel + 1
    levels.set(job.name, level);
    if (job.jobId && job.jobId !== job.name) {
      levels.set(job.jobId, level);
    }

    recursionStack.delete(jobNameOrId);
    return level;
  }

  jobs.forEach(job => {
    if (!visited.has(job.name) && !visited.has(job.jobId)) {
      dfs(job.name);
    }
  })

  return levels;
}

function onNodeClick(job: GraphNode | ActionsJob, event: MouseEvent) {
  const jobId = 'jobs' in job ? job.jobs[0]!.id : job.id;
  const link = `${props.runLink}/jobs/${jobId}`;
  if (event.ctrlKey || event.metaKey) {
    window.open(link, '_blank');
    return;
  }
  window.location.href = link;
}
</script>

<template>
  <div class="workflow-graph" v-if="jobs.length > 0">
    <div class="graph-header">
      <h4 class="graph-title">Workflow Dependencies</h4>
      <div class="graph-stats">
        {{ jobs.length }} jobs • {{ edges.length }} dependencies
        <span v-if="graphMetrics">
          • <span class="graph-metrics">{{ graphMetrics.successRate }} success</span>
        </span>
      </div>
      <div class="flex-text-block">
        <button
          type="button"
          @click="zoomIn"
          class="ui compact tiny icon button"
          :disabled="!canZoomIn"
          :title="canZoomIn ? 'Zoom in (Ctrl/Cmd + scroll on graph)' : 'Already at 100% zoom'"
        >
          <SvgIcon name="octicon-zoom-in" :size="12"/>
        </button>
        <button type="button" @click="resetView" class="ui compact tiny icon button" title="Reset view">
          <SvgIcon name="octicon-sync" :size="12"/>
        </button>
        <button type="button" @click="zoomOut" class="ui compact tiny icon button" title="Zoom out (Ctrl/Cmd + scroll on graph)">
          <SvgIcon name="octicon-zoom-out" :size="12"/>
        </button>
      </div>
    </div>

    <div
      class="graph-container"
      ref="graphContainer"
      @mousedown="handleMouseDown"
      @wheel="handleWheel"
      :class="{dragging: isDragging}"
    >
      <svg
        :width="graphWidth"
        :height="graphHeight"
        class="graph-svg"
        :style="{
          transform: `translate(${translateX}px, ${translateY}px) scale(${scale})`,
          transformOrigin: '0 0',
        }"
      >
        <defs>
          <!-- Prevent edge strokes from showing through node cards -->
          <mask :id="`workflow-graph-edge-mask-${workflowId}`">
            <rect :width="graphWidth" :height="graphHeight" fill="white"/>
            <rect
              v-for="job in jobsWithLayout"
              :key="`mask-${job.id}`"
              :x="job.x"
              :y="job.y"
              :width="nodeWidth"
              :height="job.displayHeight"
              :rx="job.type === 'job' ? 8 : 12"
              fill="black"
            />
          </mask>
        </defs>

        <g :mask="`url(#workflow-graph-edge-mask-${workflowId})`">
          <path
            v-for="bundle in outgoingBundles"
            :key="bundle.key"
            :d="bundle.path"
            fill="none"
            stroke="var(--color-secondary-alpha-50)"
            stroke-width="1.5"
            :class="['node-edge', { 'highlighted-edge': isOutgoingBundleHighlighted(bundle) }]"
          />
          <path
            v-for="edge in routedEdges"
            :key="edge.key"
            :d="edge.path"
            fill="none"
            stroke="var(--color-secondary-alpha-50)"
            stroke-width="1.5"
            :class="['node-edge', { 'highlighted-edge': isEdgeHighlighted(edge) }]"
          />
          <path
            v-for="bundle in incomingBundles"
            :key="bundle.key"
            :d="bundle.path"
            fill="none"
            stroke="var(--color-secondary-alpha-50)"
            stroke-width="1.5"
            :class="['node-edge', { 'highlighted-edge': isIncomingBundleHighlighted(bundle) }]"
          />
        </g>

        <template v-for="job in jobsWithLayout" :key="job.id">
          <g
            v-if="job.type === 'matrix'"
            class="job-node-group matrix-job-group"
            @mouseenter="handleMatrixMouseEnter(job.id)"
            @mouseleave="handleNodeMouseLeave"
          >
            <title>Matrix: {{ job.matrixKey }}</title>

            <rect
              :x="job.x"
              :y="job.y"
              :width="nodeWidth"
              :height="job.displayHeight"
              rx="12"
              fill="var(--color-box-body)"
              stroke="var(--color-secondary)"
              stroke-width="1"
              class="job-rect matrix-panel-rect"
            />

            <circle
              v-if="nodesWithIncomingEdge.has(job.id)"
              :cx="job.x"
              :cy="job.y + job.displayHeight / 2"
              r="4.5"
              class="node-port"
            />

            <circle
              v-if="nodesWithOutgoingEdge.has(job.id)"
              :cx="job.x + nodeWidth"
              :cy="job.y + job.displayHeight / 2"
              r="4.5"
              class="node-port"
            />

            <foreignObject
              :x="job.x"
              :y="job.y"
              :width="nodeWidth"
              :height="job.displayHeight"
              class="matrix-foreign-object"
            >
              <div class="matrix-panel" xmlns="http://www.w3.org/1999/xhtml" @click.stop>
                <div class="matrix-panel-tab">
                  <span class="matrix-panel-tab-label">Matrix: {{ job.matrixKey }}</span>
                </div>
                <div class="matrix-panel-body">
                  <div class="matrix-panel-summary-row">
                    <div class="matrix-row-status">
                      <ActionRunStatus :status="job.status"/>
                    </div>
                    <span class="matrix-panel-summary">
                      {{ job.jobs.length }} jobs
                      <span v-if="job.jobs.every((c) => c.status === 'success')">completed</span>
                    </span>
                  </div>
                  <button
                    type="button"
                    class="matrix-panel-toggle"
                    @click.stop="toggleMatrixExpanded(job.matrixKey!)"
                  >
                    {{ isMatrixExpanded(job.matrixKey!) ? 'Hide jobs' : 'Show all jobs' }}
                  </button>

                  <template v-if="isMatrixExpanded(job.matrixKey!)">
                    <div
                      v-for="ch in job.jobs"
                      :key="ch.id"
                      class="graph-list-row"
                      @mouseenter="handleMatrixMouseEnter(job.id)"
                      @click="onNodeClick(ch, $event)"
                    >
                      <div class="matrix-row-status">
                        <ActionRunStatus :status="ch.status"/>
                      </div>
                      <div class="graph-list-row-text">
                        <span class="graph-list-row-name">{{ ch.name }}</span>
                        <span
                          v-if="ch.duration || ch.status === 'success' || ch.status === 'failure'"
                          class="graph-list-row-duration"
                        >{{ ch.duration }}</span>
                      </div>
                    </div>
                  </template>
                </div>
              </div>
            </foreignObject>
          </g>

          <g
            v-else-if="job.type === 'group'"
            class="job-node-group grouped-job-group"
            @mouseenter="handleNodeMouseEnter(job)"
            @mouseleave="handleNodeMouseLeave"
          >
            <title>{{ job.name }}</title>

            <rect
              :x="job.x"
              :y="job.y"
              :width="nodeWidth"
              :height="job.displayHeight"
              rx="12"
              fill="var(--color-box-body)"
              stroke="var(--color-secondary)"
              stroke-width="1"
              class="job-rect grouped-panel-rect"
            />

            <circle
              v-if="nodesWithIncomingEdge.has(job.id)"
              :cx="job.x"
              :cy="job.y + job.displayHeight / 2"
              r="4.5"
              class="node-port"
            />

            <circle
              v-if="nodesWithOutgoingEdge.has(job.id)"
              :cx="job.x + nodeWidth"
              :cy="job.y + job.displayHeight / 2"
              r="4.5"
              class="node-port"
            />

            <foreignObject
              :x="job.x"
              :y="job.y"
              :width="nodeWidth"
              :height="job.displayHeight"
              class="matrix-foreign-object"
            >
              <div class="grouped-panel" xmlns="http://www.w3.org/1999/xhtml" @click.stop>
                <div
                  v-for="ch in job.jobs"
                  :key="ch.id"
                  class="graph-list-row"
                  @mouseenter="handleMatrixMouseEnter(job.id)"
                  @click="onNodeClick(ch, $event)"
                >
                  <div class="matrix-row-status">
                    <ActionRunStatus :status="ch.status"/>
                  </div>
                  <div class="graph-list-row-text">
                    <span class="graph-list-row-name">{{ ch.name }}</span>
                    <span
                      v-if="ch.duration || ch.status === 'success' || ch.status === 'failure'"
                      class="graph-list-row-duration"
                    >{{ ch.duration }}</span>
                  </div>
                </div>
              </div>
            </foreignObject>
          </g>

          <g
            v-else
            class="job-node-group"
            @click="onNodeClick(job, $event)"
            @mouseenter="handleNodeMouseEnter(job)"
            @mouseleave="handleNodeMouseLeave"
          >
            <title>{{ job.name }}</title>

            <rect
              :x="job.x"
              :y="job.y"
              :width="nodeWidth"
              :height="job.displayHeight"
              rx="8"
              fill="var(--color-box-body)"
              stroke="var(--color-secondary)"
              stroke-width="1"
              class="job-rect"
            />

            <circle
              v-if="nodesWithIncomingEdge.has(job.id)"
              :cx="job.x"
              :cy="job.y + job.displayHeight / 2"
              r="4.5"
              class="node-port"
            />

            <circle
              v-if="nodesWithOutgoingEdge.has(job.id)"
              :cx="job.x + nodeWidth"
              :cy="job.y + job.displayHeight / 2"
              r="4.5"
              class="node-port"
            />

            <foreignObject
              :x="job.x + 10"
              :y="job.y + job.displayHeight / 2 - 10"
              width="20"
              height="20"
              class="job-status-fg-obj"
            >
              <div class="job-status-icon-wrap">
                <ActionRunStatus :status="job.status"/>
              </div>
            </foreignObject>

            <foreignObject
              :x="job.x + 38"
              :y="job.y + 2"
              :width="nodeWidth - 44"
              :height="job.displayHeight - 4"
            >
              <div class="job-text-wrap">
                <span class="job-name">{{ job.name }}</span>
                <span
                  v-if="job.duration || job.status === 'success' || job.status === 'failure'"
                  class="job-duration"
                >{{ job.duration }}</span>
              </div>
            </foreignObject>

          </g>
        </template>
      </svg>
    </div>
  </div>
</template>

<style scoped>
.workflow-graph {
  flex: 1;
  display: flex;
  flex-direction: column;
}
.graph-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 14px;
  background: var(--color-box-header);
  border-bottom: 1px solid var(--color-secondary);
  gap: var(--gap-block);
  flex-wrap: wrap;
}

.graph-title {
  margin: 0;
  color: var(--color-text);
  font-size: 16px;
  font-weight: var(--font-weight-semibold);
  flex: 1;
  min-width: 200px;
}

.graph-stats {
  display: flex;
  align-items: baseline;
  column-gap: 8px;
  color: var(--color-text-light-1);
  font-size: 13px;
  white-space: nowrap;
}

.graph-metrics {
  color: var(--color-primary);
  font-weight: var(--font-weight-medium);
}

.graph-container {
  flex: 1;
  overflow: auto;
  padding: 10px 14px 18px;
  border-radius: 0 0 var(--border-radius) var(--border-radius);
  cursor: grab;
  position: relative;
  background: var(--color-box-body);
}

.graph-container.dragging {
  cursor: grabbing;
}

.graph-svg {
  display: block;
  will-change: transform;
}

.graph-svg path {
  transition: all 0.2s ease;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.highlighted-edge {
  stroke-width: 2 !important;
  stroke: var(--color-workflow-edge-hover) !important;
}

.job-node-group {
  cursor: pointer;
  transition: all 0.2s ease;
}

.job-node-group:hover .job-rect {
  /* due to SVG rendering limitation, only one of fill and drop-shadow can work */
  fill: var(--color-hover);
  /* filter: drop-shadow(0 1px 3px var(--color-shadow-opaque)); */
}

.matrix-foreign-object {
  pointer-events: auto;
}

.matrix-panel {
  width: 100%;
  height: 100%;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border-radius: 12px;
  pointer-events: auto;
  user-select: none;
}

.matrix-panel-tab {
  flex: 0 0 auto;
  padding: 5px 10px;
  background: var(--color-console-bg);
  border-bottom: 1px solid var(--color-secondary-alpha-50);
}

.matrix-panel-tab-label {
  font-size: 12px;
  font-weight: var(--font-weight-semibold);
  color: var(--color-text);
}

.matrix-panel-body {
  flex: 1 1 auto;
  padding: 6px 8px 8px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-height: 0;
}

.matrix-panel-summary-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.matrix-panel-summary {
  font-size: 11px;
  color: var(--color-text-light-2);
}

.matrix-panel-toggle {
  border: none;
  background: transparent;
  padding: 0;
  color: var(--color-primary);
  font-size: 11px;
  cursor: pointer;
  text-decoration: none;
  white-space: nowrap;
}

.matrix-panel-toggle:hover {
  text-decoration: underline;
}

.grouped-panel {
  width: 100%;
  height: 100%;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 0;
  padding: 14px 18px;
  overflow: hidden;
  border-radius: 12px;
  pointer-events: auto;
  user-select: none;
}

.graph-list-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 6px;
  min-height: 32px;
  padding: 3px 4px;
  border-radius: 6px;
  cursor: pointer;
}

.graph-list-row:hover {
  background: var(--color-hover);
}

.matrix-row-status {
  width: 18px;
  height: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
}

.graph-list-row-text {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.graph-list-row-name {
  font-size: 11px;
  font-weight: var(--font-weight-semibold);
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.graph-list-row-duration {
  font-size: 10px;
  line-height: 1.2;
  color: var(--color-text-light-2);
  white-space: nowrap;
}

.job-text-wrap {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  justify-content: center;
  gap: 1px;
  padding: 4px 8px 4px 0;
  overflow: hidden;
}

.job-name {
  width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  font-weight: var(--font-weight-semibold);
  color: var(--color-text);
  user-select: none;
  pointer-events: none;
}

.job-duration {
  font-size: 10px;
  line-height: 1.2;
  color: var(--color-text-light-2);
  white-space: nowrap;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  user-select: none;
  pointer-events: none;
}

.job-status-fg-obj,
.job-status-icon-wrap {
  pointer-events: none;
}

.job-status-icon-wrap {
  width: 20px;
  height: 20px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.node-port {
  fill: var(--color-box-body);
  stroke: var(--color-light-border);
  stroke-width: 1.25;
  opacity: 0.85;
  pointer-events: none;
}

.node-edge {
  transition: stroke-width 0.2s ease, opacity 0.2s ease;
  opacity: 0.75;
}
</style>
