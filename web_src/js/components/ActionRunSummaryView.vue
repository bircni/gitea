<script lang="ts">
import {defineComponent, type PropType} from 'vue';
import {SvgIcon} from '../svg.ts';
import ActionRunStatus from './ActionRunStatus.vue';
import WorkflowGraph from './WorkflowGraph.vue';

export default defineComponent({
  name: 'ActionRunSummaryView',
  components: {
    SvgIcon,
    ActionRunStatus,
    WorkflowGraph,
  },
  props: {
    run: {
      type: Object as PropType<Record<string, any>>,
      required: true,
    },
    artifacts: {
      type: Array as PropType<Array<Record<string, any>>>,
      required: true,
    },
    locale: {
      type: Object as PropType<Record<string, any>>,
      required: true,
    },
    runTriggeredAtIso: {
      type: String,
      required: true,
    },
    runTriggerEventLabel: {
      type: String,
      required: true,
    },
  },
});
</script>
<template>
  <div>
    <div class="action-run-summary-block">
      <p class="action-run-summary-trigger">
        {{ locale.triggeredVia }}
        <span class="tw-capitalize">{{ runTriggerEventLabel }}</span>
        &nbsp;•&nbsp;<relative-time :datetime="runTriggeredAtIso" prefix=" "/>
      </p>
      <div class="action-run-summary-context">
        <template v-if="run.commit?.pusher?.displayName">
          <a
            v-if="run.commit.pusher.link"
            class="action-run-summary-actor"
            :href="run.commit.pusher.link"
          >
            {{ run.commit.pusher.displayName }}
          </a>
          <span v-else class="action-run-summary-actor">
            {{ run.commit.pusher.displayName }}
          </span>
        </template>
        <a
          v-if="run.commit?.shortSHA"
          class="action-run-summary-sha"
          :href="run.commit.link"
        >
          <SvgIcon name="octicon-git-commit" :size="16" class="tw-mr-1"/>
          {{ run.commit.shortSHA }}
        </a>
        <span
          v-if="run.commit?.branch?.name && run.commit.branch.isDeleted"
          class="action-run-summary-branch ui label"
          :title="run.commit.branch.name"
        >
          <SvgIcon name="octicon-git-branch" :size="16" class="tw-mr-1"/>
          <span class="gt-ellipsis tw-line-through">{{ run.commit.branch.name }}</span>
        </span>
        <a
          v-else-if="run.commit?.branch?.name"
          class="action-run-summary-branch ui label"
          :href="run.commit.branch.link"
          :title="run.commit.branch.name"
        >
          <SvgIcon name="octicon-git-branch" :size="16" class="tw-mr-1"/>
          <span class="gt-ellipsis">{{ run.commit.branch.name }}</span>
        </a>
      </div>
      <p class="tw-mb-0">
        <ActionRunStatus :locale-status="locale.status[run.status]" :status="run.status" :size="16"/>
        <span class="tw-ml-2">{{ locale.status[run.status] }}</span>
        <span class="tw-ml-3">{{ locale.totalDuration }}: {{ run.duration || '–' }}</span>
        <span class="tw-ml-3">{{ locale.artifactsTitle }}: {{ artifacts.length || 0 }}</span>
      </p>
    </div>
    <WorkflowGraph
      v-if="run.jobs.length > 0"
      :jobs="run.jobs"
      :current-job-id="-1"
      :run-link="run.link"
      :workflow-id="run.workflowID"
      class="workflow-graph-container"
    />
  </div>
</template>
<style scoped>
.action-run-summary-block {
  padding: 12px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--color-secondary);
}

.action-run-summary-trigger {
  margin-bottom: 6px;
  color: var(--color-text-light-2);
}

.action-run-summary-context {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
  line-height: 1.5;
}

.action-run-summary-actor {
  display: inline-flex;
  align-items: center;
  font-weight: var(--font-weight-semibold);
  color: var(--color-text);
}

.action-run-summary-sha,
.action-run-summary-branch {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
}

.action-run-summary-sha {
  color: var(--color-text);
  font-family: var(--fonts-monospace);
  font-size: 0.875rem;
}

.action-run-summary-branch {
  gap: 0;
  border-radius: 999px;
  padding: 3px 10px;
  margin: 0;
}

.action-run-summary-branch .gt-ellipsis {
  max-width: 240px;
}
</style>
