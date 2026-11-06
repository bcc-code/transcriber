<script setup lang="ts">
import type { ModelInfo, TranscribeJob } from "~/types/job";

// `initial` prefills the form from an existing job ("Copy job").
const props = defineProps<{ initial?: TranscribeJob | null }>();
const emit = defineEmits<{ created: [job: TranscribeJob]; cancel: [] }>();

const api = useApi();

const path = ref("");
const outputPath = ref("");
const language = ref("auto");
const format = ref("all");
const model = ref("");
const priority = ref(0);
const prompt = ref("");
const callback = ref("");
const timeoutSeconds = ref<number | "">("");
const maxCharsPerLine = ref<number | "">("");
const maxLinesPerCue = ref<number | "">("");
const showAdvanced = ref(false);

// Whisper's `--prompt` is capped at ~224 tokens; ~1000 chars stays safely under.
const PROMPT_MAX = 1000;

const models = ref<ModelInfo[]>([]);
const serverPrompt = ref("");
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

const selectedModel = computed(() =>
    models.value.find((m) => m.id === model.value),
);

// What the server uses when `prompt` is blank: model prompt, then global.
const fallbackPrompt = computed(
    () => selectedModel.value?.prompt || serverPrompt.value,
);
const fallbackSource = computed(() =>
    selectedModel.value?.prompt ? "model default" : "server default",
);
const promptSupported = computed(
    () => selectedModel.value?.supports_prompt ?? true,
);

function ensureLanguageOption(code: string) {
    if (code && !LANGS.value.some((l) => l.value === code)) {
        LANGS.value.push({ value: code, label: code });
    }
}

function applyInitial(job: TranscribeJob) {
    path.value = job.path;
    outputPath.value = job.output_path;
    format.value = job.format || "all";
    priority.value = job.priority || 0;
    callback.value = job.callback || "";
    timeoutSeconds.value = job.timeout_seconds || "";
    maxCharsPerLine.value = job.subtitle_options?.max_chars_per_line || "";
    maxLinesPerCue.value = job.subtitle_options?.max_lines_per_cue || "";

    language.value = job.language || "auto";
    ensureLanguageOption(language.value);

    // Jobs store the model as requested, which may be an alias.
    const m = models.value.find(
        (m) => m.id === job.model || m.aliases.includes(job.model),
    );
    if (m) {
        model.value = m.id;
    } else if (job.model) {
        error.value = `Model “${job.model}” from the original job is no longer registered; pick another.`;
    }

    // The stored prompt is the resolved one; if it is just the default the
    // server would apply anyway, leave the field blank so it keeps tracking
    // the default instead of freezing today's copy of it.
    const p = job.prompt ?? "";
    prompt.value = p && p === fallbackPrompt.value ? "" : p;

    showAdvanced.value = Boolean(
        callback.value ||
            timeoutSeconds.value ||
            maxCharsPerLine.value ||
            maxLinesPerCue.value,
    );
}

onMounted(async () => {
    try {
        const [modelList, config] = await Promise.all([
            api.listModels(),
            api.getConfig(),
        ]);
        models.value = modelList;
        const def = models.value.find((m) => m.default);
        model.value = def?.id ?? models.value[0]?.id ?? "";
        serverPrompt.value = config.default_prompt;

        // Seed the language dropdown from the server's -default-language, so
        // a user-facing default matches what the server would have used anyway.
        if (config.default_language) {
            ensureLanguageOption(config.default_language);
            language.value = config.default_language;
        }
    } catch (e: unknown) {
        error.value = `Could not load models: ${e instanceof Error ? e.message : String(e)}`;
    }
    if (props.initial) applyInitial(props.initial);
});

function optionalInt(v: number | ""): number | undefined {
    return typeof v === "number" && v > 0 ? v : undefined;
}

async function submit() {
    if (!path.value || !outputPath.value) {
        error.value = "path and output_path are required";
        return;
    }
    submitting.value = true;
    error.value = null;
    const subtitle = {
        max_chars_per_line: optionalInt(maxCharsPerLine.value),
        max_lines_per_cue: optionalInt(maxLinesPerCue.value),
    };
    try {
        const job = await api.createJob({
            path: path.value,
            output_path: outputPath.value,
            language: language.value,
            format: format.value,
            model: model.value || undefined,
            priority: priority.value || undefined,
            prompt: (promptSupported.value && prompt.value) || undefined,
            callback: callback.value || undefined,
            timeout_seconds: optionalInt(timeoutSeconds.value),
            subtitle_options:
                subtitle.max_chars_per_line || subtitle.max_lines_per_cue
                    ? subtitle
                    : undefined,
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
        <div>
            <h2 class="text-title-1 text-text-default">
                {{ initial ? "Copy transcription job" : "New transcription job" }}
            </h2>
            <p v-if="initial" class="text-body-3 text-text-hint mt-1">
                Prefilled from
                <NuxtLink
                    :to="`/jobs/${initial.id}`"
                    class="font-mono underline hover:text-text-default"
                    >{{ initial.id.slice(0, 8) }}</NuxtLink
                >. Adjust anything, then submit to create a new job.
            </p>
        </div>

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
                label="Priority (lower runs sooner; 0 = 1000)"
                :min="0"
            />
        </div>

        <div v-if="promptSupported">
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
                    <span v-if="!prompt && fallbackPrompt">
                        Leave blank to use the {{ fallbackSource }}:
                    </span>
                    <span v-else />
                    <span class="tabular-nums">
                        {{ prompt.length }} / {{ PROMPT_MAX }}
                    </span>
                </div>
                <p
                    v-if="!prompt && fallbackPrompt"
                    class="px-3 pb-2 text-body-3 text-text-default whitespace-pre-wrap font-mono opacity-80"
                >
                    {{ fallbackPrompt }}
                </p>
            </div>
        </div>
        <p v-else class="text-body-3 text-text-hint italic">
            {{ selectedModel?.name }} doesn't use a vocabulary prompt.
        </p>

        <div>
            <button
                type="button"
                class="inline-flex items-center gap-1 text-title-3 text-text-muted hover:text-text-default"
                :aria-expanded="showAdvanced"
                @click="showAdvanced = !showAdvanced"
            >
                <Icon
                    :name="showAdvanced ? 'tabler:chevron-down' : 'tabler:chevron-right'"
                    size="16"
                />
                Advanced
            </button>
            <div
                v-if="showAdvanced"
                class="grid grid-cols-1 md:grid-cols-2 gap-4 mt-3"
            >
                <DesignInput
                    v-model="callback"
                    type="url"
                    label="Callback URL"
                    placeholder="https://…"
                    monospace
                />
                <DesignInput
                    v-model.number="timeoutSeconds"
                    type="number"
                    label="Timeout (seconds, blank = server default)"
                    :min="0"
                />
                <DesignInput
                    v-model.number="maxCharsPerLine"
                    type="number"
                    label="Subtitle max chars per line (default 42)"
                    :min="1"
                />
                <DesignInput
                    v-model.number="maxLinesPerCue"
                    type="number"
                    label="Subtitle max lines per cue (default 2)"
                    :min="1"
                />
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
