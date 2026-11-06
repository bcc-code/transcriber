<script setup lang="ts">
import type { ModelInfo } from "~/types/job";

const props = defineProps<{
    model: ModelInfo;
    defaultPrompt: string;
    usage?: { active: number; completed: number; failed: number };
}>();
const emit = defineEmits<{ saved: [model: ModelInfo] }>();

const api = useApi();

// Matches NewJobForm: whisper's `--prompt` keeps ~224 tokens.
const PROMPT_MAX = 1000;

const draft = ref(props.model.prompt);
const saving = ref(false);
const error = ref<string | null>(null);
const savedFlash = ref(false);
const copied = ref(false);

// Pick up server-side changes when the parent swaps the model object.
watch(
    () => props.model.prompt,
    (p) => (draft.value = p),
);

const dirty = computed(() => draft.value.trim() !== props.model.prompt);

async function save(value = draft.value) {
    saving.value = true;
    error.value = null;
    try {
        const updated = await api.setModelPrompt(props.model.id, value);
        draft.value = updated.prompt;
        emit("saved", updated);
        savedFlash.value = true;
        setTimeout(() => (savedFlash.value = false), 1500);
    } catch (e: unknown) {
        const err = e as { data?: { error?: string }; message?: string };
        error.value = err?.data?.error ?? err?.message ?? String(e);
    } finally {
        saving.value = false;
    }
}

async function copyId() {
    try {
        await navigator.clipboard.writeText(props.model.id);
        copied.value = true;
        setTimeout(() => (copied.value = false), 1200);
    } catch {
        // Clipboard can be blocked on non-secure origins; the id is selectable anyway.
    }
}
</script>

<template>
    <section
        class="bg-surface-raise gradient-border rounded-2xl p-5 space-y-4 shadow-resting"
    >
        <div class="flex flex-wrap items-start gap-3">
            <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                    <h2 class="text-title-1 text-text-default">
                        {{ model.name }}
                    </h2>
                    <DesignBadge v-if="model.default" variant="info">
                        default
                    </DesignBadge>
                    <DesignBadge v-if="!model.supports_prompt">
                        no prompt support
                    </DesignBadge>
                </div>
                <button
                    type="button"
                    class="mt-1 inline-flex items-center gap-1 font-mono text-body-3 text-text-muted hover:text-text-default select-all"
                    :title="copied ? 'Copied' : 'Copy model id'"
                    @click="copyId"
                >
                    {{ model.id }}
                    <Icon
                        :name="copied ? 'tabler:check' : 'tabler:copy'"
                        size="14"
                    />
                </button>
            </div>

            <div
                v-if="usage"
                class="flex items-center gap-3 text-caption-1 text-text-muted tabular-nums"
            >
                <span v-if="usage.active">{{ usage.active }} active</span>
                <span>{{ usage.completed }} completed</span>
                <span v-if="usage.failed" class="text-semantic-error">
                    {{ usage.failed }} failed
                </span>
            </div>
        </div>

        <div v-if="model.aliases.length">
            <div class="text-caption-1 text-text-hint mb-1">
                Also accepted as
            </div>
            <div class="flex flex-wrap gap-1.5">
                <DesignBadge
                    v-for="a in model.aliases"
                    :key="a"
                    class="font-mono"
                >
                    {{ a }}
                </DesignBadge>
            </div>
        </div>

        <div v-if="model.supports_prompt">
            <label
                :for="`prompt-${model.id}`"
                class="block mb-1 text-title-3 text-text-default"
            >
                Default prompt
            </label>
            <div
                class="rounded-xl border border-border-1 text-body-2 text-text-default overflow-hidden"
            >
                <textarea
                    :id="`prompt-${model.id}`"
                    v-model="draft"
                    :rows="3"
                    :maxlength="PROMPT_MAX"
                    placeholder="Names, terms, or context used for every job on this model that doesn't send its own prompt."
                    class="w-full bg-surface-default px-3 py-2 outline-none resize-y border-b border-border-1"
                />
                <div
                    class="flex items-baseline justify-between gap-2 px-3 py-2 text-caption-1 text-text-hint"
                >
                    <span>
                        <template v-if="!draft.trim() && defaultPrompt">
                            Empty — jobs fall back to the server default:
                        </template>
                        <template v-else-if="!draft.trim()">
                            Empty — jobs run without a prompt.
                        </template>
                        <template v-else>
                            Used when a job leaves its prompt blank.
                        </template>
                    </span>
                    <span class="tabular-nums">
                        {{ draft.length }} / {{ PROMPT_MAX }}
                    </span>
                </div>
                <p
                    v-if="!draft.trim() && defaultPrompt"
                    class="px-3 pb-2 text-body-3 text-text-default whitespace-pre-wrap font-mono opacity-80"
                >
                    {{ defaultPrompt }}
                </p>
            </div>

            <DesignBanner
                v-if="error"
                variant="error"
                icon="tabler:alert-circle"
                class="mt-3"
            >
                {{ error }}
            </DesignBanner>

            <div class="flex items-center justify-end gap-2 mt-3">
                <span v-if="savedFlash" class="text-caption-1 text-semantic-success">
                    Saved
                </span>
                <DesignButton
                    v-if="model.prompt"
                    variant="tertiary"
                    size="small"
                    label="Clear"
                    :disabled="saving"
                    @click="save('')"
                />
                <DesignButton
                    v-if="dirty"
                    variant="tertiary"
                    size="small"
                    label="Revert"
                    :disabled="saving"
                    @click="draft = model.prompt"
                />
                <DesignButton
                    size="small"
                    label="Save"
                    :loading="saving"
                    :disabled="!dirty"
                    @click="save()"
                />
            </div>
        </div>
    </section>
</template>
