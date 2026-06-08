<script setup lang="ts">
import type { ModelInfo, TranscribeJob } from "~/types/job";

const emit = defineEmits<{ created: [job: TranscribeJob]; cancel: [] }>();

const api = useApi();

const path = ref("");
const outputPath = ref("");
const language = ref("auto");
const format = ref("all");
const model = ref("");
const priority = ref(0);
const prompt = ref("");

// Whisper's `--prompt` is capped at ~224 tokens; ~1000 chars stays safely under.
const PROMPT_MAX = 1000;

const models = ref<ModelInfo[]>([]);
const defaultPrompt = ref("");
const submitting = ref(false);
const error = ref<string | null>(null);

const LANGS = ref([
    { value: "auto", label: "auto-detect" },
    { value: "no", label: "Norwegian" },
    { value: "en", label: "English" },
    { value: "de", label: "German" },
    { value: "nl", label: "Dutch" },
]);

const FORMATS = [
    { value: "all", label: "all (json+srt+vtt+txt)" },
    { value: "json", label: "json" },
    { value: "srt", label: "srt" },
    { value: "vtt", label: "vtt" },
    { value: "txt", label: "txt" },
];

const modelOptions = computed(() =>
    models.value.map((m) => ({
        value: m.id,
        label: m.name + (m.default ? " · default" : ""),
    })),
);

onMounted(async () => {
    try {
        const [modelList, config] = await Promise.all([
            api.listModels(),
            api.getConfig(),
        ]);
        models.value = modelList;
        const def = models.value.find((m) => m.default);
        model.value = def?.id ?? models.value[0]?.id ?? "";
        defaultPrompt.value = config.default_prompt;

        // Seed the language dropdown from the server's -default-language, so
        // a user-facing default matches what the server would have used anyway.
        // If the configured code isn't in our static list, surface it so the
        // current selection isn't a phantom value.
        if (config.default_language) {
            if (!LANGS.value.some((l) => l.value === config.default_language)) {
                LANGS.value.push({
                    value: config.default_language,
                    label: config.default_language,
                });
            }
            language.value = config.default_language;
        }
    } catch (e: unknown) {
        error.value = `Could not load models: ${e instanceof Error ? e.message : String(e)}`;
    }
});

async function submit() {
    if (!path.value || !outputPath.value) {
        error.value = "path and output_path are required";
        return;
    }
    submitting.value = true;
    error.value = null;
    try {
        const job = await api.createJob({
            path: path.value,
            output_path: outputPath.value,
            language: language.value,
            format: format.value,
            model: model.value || undefined,
            priority: priority.value || undefined,
            prompt: prompt.value || undefined,
        });
        emit("created", job);
    } catch (e: unknown) {
        const err = e as { data?: { error?: string }; message?: string };
        error.value = err?.data?.error ?? err?.message ?? String(e);
    } finally {
        submitting.value = false;
    }
}
</script>

<template>
    <form
        class="bg-surface-raise gradient-border rounded-2xl p-6 space-y-4 shadow-resting"
        @submit.prevent="submit"
    >
        <h2 class="text-title-1 text-text-default">New transcription job</h2>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <DesignInput
                v-model="path"
                label="Audio file path"
                placeholder="/mnt/storage/audio/foo.wav"
                required
                monospace
            />
            <DesignInput
                v-model="outputPath"
                label="Output directory"
                placeholder="/mnt/storage/out/foo/"
                required
                monospace
            />
            <DesignInput
                v-model="model"
                type="select"
                label="Model"
                prompt="(server default)"
                :options="modelOptions"
            />
            <DesignInput
                v-model="language"
                type="select"
                label="Language"
                :options="LANGS"
            />
            <DesignInput
                v-model="format"
                type="select"
                label="Format"
                :options="FORMATS"
            />
            <DesignInput
                v-model.number="priority"
                type="number"
                label="Priority"
                :min="0"
                :max="10"
            />
        </div>

        <div>
            <label class="block mb-1 text-title-3 text-text-default">
                Vocabulary prompt
            </label>
            <div
                class="rounded-xl border border-border-1 text-body-2 text-text-default overflow-hidden"
            >
                <textarea
                    v-model="prompt"
                    placeholder="Names, terms, or context to bias the decoder (e.g. Anders, Knut, Bibelen)."
                    :rows="4"
                    :maxlength="PROMPT_MAX"
                    class="w-full bg-surface-default px-3 py-2 outline-none resize-y border-b border-border-1"
                />
                <div
                    class="flex items-baseline justify-between gap-2 px-3 py-2 text-caption-1 text-text-hint"
                >
                    <span v-if="!prompt && defaultPrompt">
                        Leave blank to use the server default:
                    </span>
                    <span v-else />
                    <span class="tabular-nums">
                        {{ prompt.length }} / {{ PROMPT_MAX }}
                    </span>
                </div>
                <p
                    v-if="!prompt && defaultPrompt"
                    class="px-3 pb-2 text-body-3 text-text-default whitespace-pre-wrap font-mono opacity-80"
                >
                    {{ defaultPrompt }}
                </p>
            </div>
        </div>

        <DesignBanner v-if="error" variant="error" icon="tabler:alert-circle">
            {{ error }}
        </DesignBanner>

        <div class="flex items-center justify-end gap-2">
            <DesignButton
                variant="tertiary"
                label="Cancel"
                @click="emit('cancel')"
            />
            <DesignButton
                type="submit"
                :loading="submitting"
                :label="submitting ? 'Submitting…' : 'Submit'"
            />
        </div>
    </form>
</template>
