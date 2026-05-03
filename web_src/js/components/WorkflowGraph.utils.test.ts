import {computeGraphHighlightState, createWorkflowGraphModel, computeJobLevels, matrixKeyFromJobName} from './WorkflowGraph.utils.ts';
import type {ActionsJob} from '../modules/gitea-actions.ts';

const mockJobs: ActionsJob[] = [
  {id: 1, link: '', jobId: 'job-100', name: 'job-100', status: 'success', canRerun: false, duration: '3s'},
  {id: 2, link: '', jobId: 'job-101', name: 'job-101', status: 'success', canRerun: false, duration: '3s', needs: ['job-100']},
  {id: 3, link: '', jobId: 'job-102', name: 'job-102', status: 'success', canRerun: false, duration: '4s', needs: ['job-101']},
  {id: 4, link: '', jobId: 'job-103', name: 'job-103', status: 'success', canRerun: false, duration: '2s', needs: ['job-100']},
  {id: 5, link: '', jobId: 'prep-jdk', name: 'prep-jdk', status: 'success', canRerun: false, duration: '3s'},
  {id: 6, link: '', jobId: 'code-analysis', name: 'code-analysis', status: 'success', canRerun: false, duration: '3s'},
  {id: 7, link: '', jobId: 'matrix-e2e-1-chromium', name: 'matrix-e2e (1, chromium)', status: 'success', canRerun: false, duration: '2s', needs: ['job-100', 'prep-jdk', 'code-analysis']},
  {id: 8, link: '', jobId: 'matrix-e2e-1-firefox', name: 'matrix-e2e (1, firefox)', status: 'success', canRerun: false, duration: '2s', needs: ['job-100', 'prep-jdk', 'code-analysis']},
  {id: 9, link: '', jobId: 'matrix-e2e-2-chromium', name: 'matrix-e2e (2, chromium)', status: 'success', canRerun: false, duration: '2s', needs: ['job-100', 'prep-jdk', 'code-analysis']},
  {id: 10, link: '', jobId: 'matrix-e2e-3-chromium', name: 'matrix-e2e (3, chromium)', status: 'success', canRerun: false, duration: '4s', needs: ['job-100', 'prep-jdk', 'code-analysis']},
  {id: 11, link: '', jobId: 'matrix-e2e-3-firefox', name: 'matrix-e2e (3, firefox)', status: 'success', canRerun: false, duration: '2s', needs: ['job-100', 'prep-jdk', 'code-analysis']},
  {id: 12, link: '', jobId: 'matrix-e2e-99-webkit', name: 'matrix-e2e (99, webkit)', status: 'success', canRerun: false, duration: '2s', needs: ['job-100', 'prep-jdk', 'code-analysis']},
  {id: 13, link: '', jobId: 'unit-test', name: 'unit-test', status: 'success', canRerun: false, duration: '3s', needs: ['prep-jdk', 'code-analysis']},
  {id: 14, link: '', jobId: 'arch-test', name: 'arch-test', status: 'success', canRerun: false, duration: '3s', needs: ['prep-jdk', 'code-analysis']},
  {id: 15, link: '', jobId: 'integration-test', name: 'integration-test', status: 'success', canRerun: false, duration: '4s', needs: ['prep-jdk', 'code-analysis']},
  {id: 16, link: '', jobId: 'build-image', name: 'build-image', status: 'success', canRerun: false, duration: '3s', needs: [
    'unit-test',
    'arch-test',
    'integration-test',
    'matrix-e2e-1-chromium',
    'matrix-e2e-1-firefox',
    'matrix-e2e-2-chromium',
    'matrix-e2e-3-chromium',
    'matrix-e2e-3-firefox',
    'matrix-e2e-99-webkit',
  ]},
];

const verifyDeployJobs: ActionsJob[] = [
  {id: 101, link: '', jobId: 'seed-dev', name: 'seed-dev', status: 'success', canRerun: false, duration: '2s'},
  {id: 102, link: '', jobId: 'seed-qa', name: 'seed-qa', status: 'success', canRerun: false, duration: '3s'},
  {id: 103, link: '', jobId: 'verify-dev', name: 'Verify Dev', status: 'success', canRerun: false, duration: '3s', needs: ['seed-dev']},
  {id: 104, link: '', jobId: 'verify-qa', name: 'Verify QA', status: 'success', canRerun: false, duration: '4s', needs: ['seed-qa']},
  {id: 105, link: '', jobId: 'deploy', name: 'Deploy', status: 'blocked', canRerun: false, duration: '', needs: ['verify-dev', 'verify-qa']},
];

const simpleFanoutJobs: ActionsJob[] = [
  {id: 201, link: '', jobId: 'job-100', name: 'job-100', status: 'success', canRerun: false, duration: '3s'},
  {id: 202, link: '', jobId: 'job-101', name: 'job-101', status: 'success', canRerun: false, duration: '3s', needs: ['job-100']},
  {id: 203, link: '', jobId: 'job-103', name: 'job-103', status: 'success', canRerun: false, duration: '2s', needs: ['job-100']},
];

test('matrix key heuristic keeps GitHub-style prefix', () => {
  expect(matrixKeyFromJobName('matrix-e2e (1, chromium)')).toBe('matrix-e2e');
  expect(matrixKeyFromJobName('plain-job')).toBeNull();
});

test('computeJobLevels keeps stable topological levels', () => {
  const levels = computeJobLevels(mockJobs);
  expect(levels.get('job-100')).toBe(0);
  expect(levels.get('job-101')).toBe(1);
  expect(levels.get('job-102')).toBe(2);
  expect(levels.get('build-image')).toBe(2);
});

test('graph model collapses matrix and groups parallel test jobs', () => {
  const graph = createWorkflowGraphModel(mockJobs);

  expect(graph.nodes.find((node) => node.type === 'matrix')?.jobs).toHaveLength(6);
  expect(graph.nodes.find((node) => node.type === 'group')?.jobs.map((job) => job.jobId)).toEqual([
    'unit-test',
    'arch-test',
    'integration-test',
  ]);
});

test('expanded matrix height includes summary and toggle rows', () => {
  const collapsed = createWorkflowGraphModel(mockJobs);
  const expanded = createWorkflowGraphModel(mockJobs, new Set(['matrix-e2e']));
  const collapsedMatrix = collapsed.nodes.find((node) => node.id === 'matrix:matrix-e2e');
  const expandedMatrix = expanded.nodes.find((node) => node.id === 'matrix:matrix-e2e');

  expect(collapsedMatrix?.displayHeight).toBeLessThan(expandedMatrix?.displayHeight ?? 0);
  expect(expandedMatrix?.displayHeight).toBe(264);
});

test('lane placement keeps main chain on top and grouped work below', () => {
  const graph = createWorkflowGraphModel(mockJobs);
  const nodes = new Map(graph.nodes.map((node) => [node.id, node]));

  const job101 = nodes.get('job:2');
  const job102 = nodes.get('job:3');
  const job103 = nodes.get('job:4');
  const matrix = nodes.get('matrix:matrix-e2e');
  const group = Array.from(nodes.values()).find((node) => node.type === 'group');
  const buildImage = nodes.get('job:16');

  expect(job101?.y).toBeLessThan(job103?.y ?? 0);
  expect(job101?.y).toBeLessThan(matrix?.y ?? 0);
  expect(matrix?.y).toBeLessThan(group?.y ?? 0);
  expect(job102?.y).toBeLessThan(buildImage?.y ?? 0);
  expect(nodes.get('job:5')?.y).toBeLessThan(nodes.get('job:6')?.y ?? 0);
});

test('bundled routes stay orthogonal and include bundle stubs', () => {
  const graph = createWorkflowGraphModel(mockJobs);
  const matrixIncomingBundle = graph.incomingBundles.find((bundle) => bundle.toId === 'matrix:matrix-e2e');
  const buildImageIncomingBundle = graph.incomingBundles.find((bundle) => bundle.toId === 'job:16');
  const lowerClusterOutgoingBundle = graph.outgoingBundles.find((bundle) => bundle.fromId === 'job:5');
  const buildImageEdge = graph.routedEdges.find((edge) => edge.fromId === 'matrix:matrix-e2e' && edge.toId === 'job:16');
  const matrixIncomingEdges = graph.edges.filter((edge) => edge.toId === 'matrix:matrix-e2e').map((edge) => edge.fromId).sort();
  const groupIncomingEdges = graph.edges.filter((edge) => edge.toId.includes('group:')).map((edge) => edge.fromId);

  expect(lowerClusterOutgoingBundle?.toIds.sort()).toEqual(['group:1:code-analysis\u0001prep-jdk:build-image', 'matrix:matrix-e2e']);
  expect(lowerClusterOutgoingBundle?.edgeKeys.sort()).toEqual([
    'job:5->group:1:code-analysis\u0001prep-jdk:build-image',
    'job:5->matrix:matrix-e2e',
  ]);
  expect(matrixIncomingEdges).toEqual(['job:5', 'job:6']);
  expect(groupIncomingEdges).toEqual(['job:5']);
  expect(matrixIncomingBundle?.fromIds.slice().sort()).toEqual(['job:5', 'job:6']);
  expect(matrixIncomingBundle?.edgeKeys.sort()).toEqual(['job:5->matrix:matrix-e2e', 'job:6->matrix:matrix-e2e']);
  expect(buildImageIncomingBundle?.fromIds).toHaveLength(2);
  expect(buildImageEdge?.path).toMatch(/^M [\d.]+ [\d.]+ H [\d.]+$/);
});

test('verify-deploy graph keeps direct edges flat and deploy merge local', () => {
  const graph = createWorkflowGraphModel(verifyDeployJobs);
  const nodes = new Map(graph.nodes.map((node) => [node.id, node]));
  const verifyDevEdge = graph.routedEdges.find((edge) => edge.fromId === 'job:101' && edge.toId === 'job:103');
  const verifyQaEdge = graph.routedEdges.find((edge) => edge.fromId === 'job:102' && edge.toId === 'job:104');
  const deployUpperEdge = graph.routedEdges.find((edge) => edge.fromId === 'job:103' && edge.toId === 'job:105');
  const deployLowerEdge = graph.routedEdges.find((edge) => edge.fromId === 'job:104' && edge.toId === 'job:105');
  const verifyDev = nodes.get('job:103');
  const deploy = nodes.get('job:105');

  expect(verifyDevEdge?.path).toMatch(/^M [\d.]+ [\d.]+ H [\d.]+$/);
  expect(verifyQaEdge?.path).toMatch(/^M [\d.]+ [\d.]+ H [\d.]+$/);
  expect(deployUpperEdge?.path).toMatch(/^M [\d.]+ [\d.]+ H [\d.]+$/);
  expect(deployLowerEdge?.path).toContain('V');
  expect(deploy).toBeDefined();
  expect(verifyDev).toBeDefined();
  expect(deploy!.y).toBe(verifyDev!.y);
});

test('fanout branch peels off directly from the source stub', () => {
  const graph = createWorkflowGraphModel(simpleFanoutJobs);
  const straightEdge = graph.routedEdges.find((edge) => edge.fromId === 'job:201' && edge.toId === 'job:202');
  const branchEdge = graph.routedEdges.find((edge) => edge.fromId === 'job:201' && edge.toId === 'job:203');

  expect(straightEdge?.path).toMatch(/^M [\d.]+ [\d.]+ H [\d.]+$/);
  expect(branchEdge?.path).toMatch(/^M [\d.]+ [\d.]+ V [\d.]+ Q [\d.]+ [\d.]+ [\d.]+ [\d.]+ H [\d.]+$/);
});

test('directed highlight state excludes siblings that only share descendants', () => {
  const graph = createWorkflowGraphModel(mockJobs);

  const prepHighlight = computeGraphHighlightState('job:5', graph.edges);
  expect(prepHighlight.nodeIds.has('job:5')).toBe(true);
  expect(prepHighlight.nodeIds.has('matrix:matrix-e2e')).toBe(true);
  expect(prepHighlight.nodeIds.has('group:1:code-analysis\u0001prep-jdk:build-image')).toBe(true);
  expect(prepHighlight.nodeIds.has('job:16')).toBe(true);
  expect(prepHighlight.nodeIds.has('job:6')).toBe(false);
  expect(prepHighlight.edgeKeys.has('job:5->matrix:matrix-e2e')).toBe(true);
  expect(prepHighlight.edgeKeys.has('job:5->group:1:code-analysis\u0001prep-jdk:build-image')).toBe(true);
  expect(prepHighlight.edgeKeys.has('job:6->matrix:matrix-e2e')).toBe(false);

  const codeHighlight = computeGraphHighlightState('job:6', graph.edges);
  expect(codeHighlight.nodeIds.has('job:5')).toBe(false);
  expect(codeHighlight.edgeKeys.has('job:5->matrix:matrix-e2e')).toBe(false);
  expect(codeHighlight.edgeKeys.has('job:6->matrix:matrix-e2e')).toBe(true);
});

test('directed highlight state for converging graph excludes sibling branch when hovering parent', () => {
  const graph = createWorkflowGraphModel(verifyDeployJobs);

  const parentHighlight = computeGraphHighlightState('job:103', graph.edges);
  expect(parentHighlight.nodeIds.has('job:101')).toBe(true);
  expect(parentHighlight.nodeIds.has('job:105')).toBe(true);
  expect(parentHighlight.nodeIds.has('job:104')).toBe(false);
  expect(parentHighlight.edgeKeys.has('job:103->job:105')).toBe(true);
  expect(parentHighlight.edgeKeys.has('job:104->job:105')).toBe(false);

  const sinkHighlight = computeGraphHighlightState('job:105', graph.edges);
  expect(sinkHighlight.nodeIds.has('job:103')).toBe(true);
  expect(sinkHighlight.nodeIds.has('job:104')).toBe(true);
  expect(sinkHighlight.edgeKeys.has('job:103->job:105')).toBe(true);
  expect(sinkHighlight.edgeKeys.has('job:104->job:105')).toBe(true);
});
