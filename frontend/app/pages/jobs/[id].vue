<script setup lang="ts">
const route = useRoute();
const id = computed(() => route.params.id as string);

const { job, loading, error, fetchOnce } = useJob(id);
const api = useApi();
const canceling = ref(false);

const canCancel = computed(
    () => job.value?.status === "QUEUED" || job.value?.status === "RUNNING",
);

async function onCancel() {
    canceling.value = true;
    try {
        await api.cancelJob(id.value);
        await fetchOnce();
    } finally {
        canceling.value = false;
    }
}
</script>

<template>
    <div>
        <DesignButton
            to="/"
            variant="tertiary"
            size="small"
            icon="tabler:arrow-left"
            label="Back to jobs"
            class="mb-4"
        />

        <DesignBanner v-if="error" variant="error" icon="tabler:alert-circle">
            {{ error }}
        </DesignBanner>

        <DesignLoadingState v-else-if="!job && loading" :size="32" />

        <div
            v-else-if="job"
            class="bg-surface-raise gradient-border rounded-3xl p-6 space-y-6 shadow-resting"
            :style="{ 'view-transition-name': `job-${job.id}` }"
        >
            <div class="flex items-center justify-between">
                <div class="flex items-center gap-3">
                    <JobStatusBadge :status="job.status" />
                    <span class="font-mono text-body-3 text-text-hint">{{
                        job.id
                    }}</span>
                </div>
                <div class="flex items-center gap-2">
                    <DesignButton
                        :to="`/?copy=${job.id}`"
                        variant="secondary"
                        size="small"
                        icon="tabler:copy"
                        label="Copy job"
                    />
                    <DesignButton
                        v-if="canCancel"
                        variant="danger"
                        size="small"
                        icon="tabler:circle-x"
                        label="Cancel"
                        :loading="canceling"
                        @click="onCancel"
                    />
                </div>
            </div>

            <div v-if="job.status === 'RUNNING'">
                <div
                    class="flex items-center justify-between mb-1 text-caption-1 text-text-muted"
                >
                    <span>Progress</span>
                    <span>{{ job.progress }}%</span>
                </div>
                <ProgressBar :value="job.progress" />
            </div>

            <dl class="grid grid-cols-1 md:grid-cols-2 gap-x-8 gap-y-4">
                <Detail label="Input">{{ job.path }}</Detail>
                <Detail label="Output dir">{{ job.output_path }}</Detail>
                <Detail label="Model">{{ job.model || "—" }}</Detail>
                <Detail label="Language">{{ job.language || "—" }}</Detail>
                <Detail label="Format">{{ job.format || "—" }}</Detail>
                <Detail label="Priority">{{ job.priority }}</Detail>
                <Detail label="Duration">{{ job.duration || "—" }}</Detail>
                <Detail label="Callback">{{ job.callback || "—" }}</Detail>
                <Detail v-if="job.timeout_seconds" label="Timeout">
                    {{ job.timeout_seconds }}s
                </Detail>
                <Detail v-if="job.subtitle_options" label="Subtitles">
                    {{ job.subtitle_options.max_chars_per_line ?? 42 }} chars/line ·
                    {{ job.subtitle_options.max_lines_per_cue ?? 2 }} lines/cue
                </Detail>
            </dl>

            <div v-if="job.prompt" class="border-t border-border-1 pt-4">
                <Detail label="Vocabulary prompt">{{ job.prompt }}</Detail>
            </div>

            <div
                v-if="job.results?.length || job.result"
                class="border-t border-border-1 pt-4"
            >
                <Detail :label="`Result files (${(job.results ?? [job.result]).length})`">
                    <ul class="space-y-1">
                        <li v-for="p in job.results ?? [job.result]" :key="p">
                            {{ p }}
                        </li>
                    </ul>
                </Detail>
            </div>

            <DesignBanner
                v-if="job.error"
                variant="error"
                icon="tabler:alert-circle"
            >
                <span class="font-mono">{{ job.error }}</span>
            </DesignBanner>
        </div>
    </div>
</template>
