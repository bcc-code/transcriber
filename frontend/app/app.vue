<script setup lang="ts">
const { theme, toggle } = useTheme();

// Build identity of the running server, so you can tell which commit is deployed.
const { data: version } = useAsyncData("version", () => useApi().getVersion(), {
    server: false,
});
const shortCommit = computed(() => {
    const c = version.value?.commit;
    if (!c) return "";
    const s = c === "unknown" ? c : c.slice(0, 7);
    return version.value?.modified ? `${s}-dirty` : s;
});
</script>

<template>
    <div class="min-h-screen bg-surface-indent text-text-default">
        <header class="bg-surface-default border-b border-border-1">
            <div class="mx-auto max-w-6xl px-6 py-2 flex items-center gap-3">
                <NuxtLink to="/" class="text-title-2 items-center flex gap-2">
                    <AppLogo class="size-4.5" />
                    Transcriber
                </NuxtLink>
                <nav class="ml-auto flex items-center gap-4 text-body-3">
                    <span
                        v-if="shortCommit"
                        class="font-mono text-text-muted"
                        :title="`Commit ${version?.commit}${version?.build_time ? `, built ${version.build_time}` : ''}`"
                    >
                        {{ shortCommit }}
                    </span>
                    <DesignButton
                        :aria-label="`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`"
                        :title="`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`"
                        :icon="theme === 'dark' ? 'tabler:sun' : 'tabler:moon'"
                        variant="tertiary"
                        @click="toggle"
                    />
                </nav>
            </div>
        </header>

        <main class="mx-auto max-w-6xl px-6 py-8">
            <NuxtRouteAnnouncer />
            <NuxtPage />
        </main>
    </div>
</template>
