<script setup lang="ts">
import type { TranscribeJob } from "~/types/job";

const props = defineProps<{ job: TranscribeJob }>();
const emit = defineEmits<{ cancel: [] }>();
const router = useRouter();

function onCopy(e: Event) {
    e.preventDefault();
    e.stopPropagation();
    router.push({ path: "/", query: { copy: props.job.id } });
}

const api = useApi();
const canceling = ref(false);

const canCancel = computed(
    () => props.job.status === "QUEUED" || props.job.status === "RUNNING",
);

async function onCancel(e: Event) {
    e.preventDefault();
    e.stopPropagation();
    if (!canCancel.value) return;
    canceling.value = true;
    try {
        await api.cancelJob(props.job.id);
        emit("cancel");
    } finally {
        canceling.value = false;
    }
}
</script>

<template>
    <NuxtLink
        :to="`/jobs/${job.id}`"
        class="block bg-surface-raise gradient-border shadow-resting hover:shadow-floating rounded-2xl transition-shadow p-4"
        :style="{ 'view-transition-name': `job-${job.id}` }"
    >
        <div class="flex items-center gap-4">
            <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 mb-1">
                    <JobStatusBadge :status="job.status" />
                    <span class="text-caption-1 text-text-hint font-mono">{{
                        job.id.slice(0, 8)
                    }}</span>
                    <span class="text-caption-1 text-text-hint">·</span>
                    <span class="text-caption-1 text-text-muted">{{
                        job.model || "—"
                    }}</span>
                    <template v-if="job.language">
                        <span class="text-caption-1 text-text-hint">·</span>
                        <span class="text-caption-1 text-text-muted">{{
                            job.language
                        }}</span>
                    </template>
                </div>
                <div class="text-body-3 text-text-default truncate font-mono">
                    {{ job.path }}
                </div>
            </div>

            <div class="flex items-center gap-3 shrink-0">
                <ProgressBar
                    v-if="job.status === 'RUNNING'"
                    :value="job.progress"
                    class="w-28"
                />
                <span
                    v-else-if="job.status === 'COMPLETED' && job.duration"
                    class="text-caption-1 text-text-hint"
                >
                    {{ job.duration }}
                </span>
                <DesignButton
                    variant="tertiary"
                    title="Copy job"
                    aria-label="Copy job"
                    class="text-text-hint hover:text-text-default"
                    icon="tabler:copy"
                    @click="onCopy"
                />
                <DesignButton
                    v-if="canCancel"
                    variant="tertiary"
                    :loading="canceling"
                    title="Cancel job"
                    aria-label="Cancel job"
                    class="text-text-hint hover:text-semantic-error!"
                    icon="tabler:circle-x"
                    @click="onCancel"
                />
            </div>
        </div>
    </NuxtLink>
</template>
