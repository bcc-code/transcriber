<script setup lang="ts">
import type { TranscribeJob } from "~/types/job";

const { jobs, loading, error, fetchOnce } = useJobsList();
const api = useApi();
const cache = useJobsCache();
const route = useRoute();
const router = useRouter();

const showForm = ref(false);
// Job to prefill the form with, from `/?copy=<id>`.
const copyFrom = ref<TranscribeJob | null>(null);
const copyError = ref<string | null>(null);

watch(
    () => route.query.copy,
    async (id) => {
        copyError.value = null;
        if (typeof id !== "string" || !id) {
            copyFrom.value = null;
            return;
        }
        try {
            copyFrom.value = cache.get(id) ?? (await api.getJob(id));
            showForm.value = true;
        } catch (e: unknown) {
            copyFrom.value = null;
            copyError.value = `Could not load job ${id} to copy: ${e instanceof Error ? e.message : String(e)}`;
        }
    },
    { immediate: true },
);

function closeForm() {
    showForm.value = false;
    if (route.query.copy) router.replace({ query: {} });
}

function toggleForm() {
    if (showForm.value) closeForm();
    else showForm.value = true;
}

const activeJobs = computed(() =>
    jobs.value.filter(
        (j) => j.status === "QUEUED" || j.status === "RUNNING",
    ),
);
const recentJobs = computed(() =>
    jobs.value.filter(
        (j) =>
            j.status === "COMPLETED" ||
            j.status === "FAILED" ||
            j.status === "CANCELED",
    ),
);

async function onCreated(_job: TranscribeJob) {
    closeForm();
    await fetchOnce();
}
</script>

<template>
    <div>
        <DesignHeader>
            Transcription jobs
            <template #subtitle>
                {{ activeJobs.length }} active · {{ recentJobs.length }} recent
                <span v-if="loading" class="ml-2 text-text-hint"
                    >refreshing…</span
                >
            </template>
            <template #actions>
                <DesignButton
                    :variant="showForm ? 'secondary' : 'primary'"
                    :icon="showForm ? 'tabler:x' : 'tabler:plus'"
                    :label="showForm ? 'Close' : 'New job'"
                    @click="toggleForm"
                />
            </template>
        </DesignHeader>

        <DesignBanner
            v-if="copyError"
            variant="error"
            icon="tabler:alert-circle"
            class="mb-4"
        >
            {{ copyError }}
        </DesignBanner>

        <!-- Keyed so switching the copy source remounts with fresh values. -->
        <NewJobForm
            v-if="showForm"
            :key="copyFrom?.id ?? 'new'"
            :initial="copyFrom"
            class="mb-6"
            @created="onCreated"
            @cancel="closeForm"
        />

        <DesignBanner
            v-if="error"
            variant="error"
            icon="tabler:alert-circle"
            class="mb-4"
        >
            {{ error }}
        </DesignBanner>

        <DesignEmptyState
            v-if="!jobs.length && !loading"
            icon="tabler:wave-saw-tool"
            title="No jobs yet"
            description="Click “New job” to start a transcription."
            class="py-16"
        />

        <div v-else class="space-y-8">
            <section>
                <h2
                    class="text-caption-1 font-medium text-text-muted uppercase tracking-wider mb-3"
                >
                    Active
                    <span class="text-text-hint ml-1"
                        >({{ activeJobs.length }})</span
                    >
                </h2>
                <div v-if="activeJobs.length" class="space-y-2">
                    <JobRow
                        v-for="j in activeJobs"
                        :key="j.id"
                        :job="j"
                        @cancel="fetchOnce"
                    />
                </div>
                <p v-else class="text-body-3 text-text-hint italic">
                    No active jobs.
                </p>
            </section>

            <section v-if="recentJobs.length">
                <h2
                    class="text-caption-1 font-medium text-text-muted uppercase tracking-wider mb-3"
                >
                    Recent
                    <span class="text-text-hint ml-1"
                        >({{ recentJobs.length }})</span
                    >
                </h2>
                <div class="space-y-2">
                    <JobRow
                        v-for="j in recentJobs"
                        :key="j.id"
                        :job="j"
                        @cancel="fetchOnce"
                    />
                </div>
            </section>
        </div>
    </div>
</template>
