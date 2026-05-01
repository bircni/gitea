import {createWorkflowGraphModel, computeJobLevels, matrixKeyFromJobName} from './WorkflowGraph.utils.ts';
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
});

test('bundled routes stay orthogonal and include bundle stubs', () => {
  const graph = createWorkflowGraphModel(mockJobs);
  const matrixIncomingBundle = graph.incomingBundles.find((bundle) => bundle.toId === 'matrix:matrix-e2e');
  const buildImageIncomingBundle = graph.incomingBundles.find((bundle) => bundle.toId === 'job:16');
  const buildImageEdge = graph.routedEdges.find((edge) => edge.fromId === 'matrix:matrix-e2e' && edge.toId === 'job:16');
  const matrixIncomingEdges = graph.edges.filter((edge) => edge.toId === 'matrix:matrix-e2e').map((edge) => edge.fromId).sort();
  const groupIncomingEdges = graph.edges.filter((edge) => edge.toId.includes('group:')).map((edge) => edge.fromId);

  expect(graph.outgoingBundles).toHaveLength(0);
  expect(matrixIncomingEdges).toEqual(['job:5', 'job:6']);
  expect(groupIncomingEdges).toEqual(['job:5']);
  expect(matrixIncomingBundle?.fromIds).toEqual(['job:5', 'job:6']);
  expect(buildImageIncomingBundle?.fromIds).toHaveLength(2);
  expect(buildImageEdge?.path).toContain('H');
  expect(buildImageEdge?.path).toContain('V');
});
