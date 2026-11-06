<script setup lang="ts">
import type { ModelInfo } from "~/types/job";

const api = useApi();
const { jobs } = useJobsList();

const models = ref<ModelInfo[]>([]);
const defaultPrompt = ref("");
const loading = ref(true);
const error = ref<string | null>(null);

onMounted(async () => {
    try {
        const [list, config] = await Promise.all([
            api.listModels(),
            api.getConfig(),
        ]);
        // Default model first, then alphabetical (server already sorts by id).
        models.value = list.sort((a, b) => Number(b.default) - Number(a.default));
        defaultPrompt.value = config.default_prompt;
    } catch (e: unknown) {
        error.value = e instanceof Error ? e.message : String(e);
    } finally {
        loading.value = false;
    }
});

// Jobs may reference a model by alias, so resolve aliases to the canonical id.
const usage = computed(() => {
    const canonical = new Map<string, string>();
    for (const m of models.value) {
        canonical.set(m.id, m.id);
        for (const a of m.aliases) canonical.set(a, m.id);
    }
    const out: Record<string, { active: number; completed: number; failed: number }> = {};
    for (const j of jobs.value) {
        const id = canonical.get(j.model) ?? j.model;
        const u = (out[id] ??= { active: 0, completed: 0, failed: 0 });
        if (j.status === "QUEUED" || j.status === "RUNNING") u.active++;
        else if (j.status === "COMPLETED") u.completed++;
        else if (j.status === "FAILED") u.failed++;
    }
    return out;
});

function onSaved(updated: ModelInfo) {
    const i = models.value.findIndex((m) => m.id === updated.id);
    if (i >= 0) models.value[i] = updated;
}
</script>

<template>
    <div>
        <DesignHeader>
            Models
            <template #subtitle>
                {{ models.length }} registered
            </template>
        </DesignHeader>

        <DesignBanner
            v-if="error"
            variant="error"
            icon="tabler:alert-circle"
            class="mb-4"
        >
            {{ error }}
        </DesignBanner>

        <DesignLoadingState v-if="loading" :size="32" />

        <DesignEmptyState
            v-else-if="!models.length && !error"
            icon="tabler:cpu"
            title="No models registered"
            description="Models are configured in cmd/transcriber/models.go."
            class="py-16"
        />

        <div v-else class="space-y-4">
            <ModelCard
                v-for="m in models"
                :key="m.id"
                :model="m"
                :default-prompt="defaultPrompt"
                :usage="usage[m.id]"
                @saved="onSaved"
            />
        </div>
    </div>
</template>
