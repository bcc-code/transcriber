<script setup lang="ts">
const { theme, toggle } = useTheme();
const route = useRoute();

const NAV = [
    { to: "/", label: "Jobs" },
    // Not "/models": that path is the JSON API endpoint.
    { to: "/model-settings", label: "Models" },
];

// "/" also owns /jobs/:id; everything else matches by prefix.
function isActive(to: string) {
    if (to === "/") return route.path === "/" || route.path.startsWith("/jobs");
    return route.path.startsWith(to);
}
</script>

<template>
    <div class="min-h-screen bg-surface-indent text-text-default">
        <header class="bg-surface-default border-b border-border-1">
            <div class="mx-auto max-w-6xl px-6 py-2 flex items-center gap-3">
                <NuxtLink to="/" class="text-title-2 items-center flex gap-2">
                    <AppLogo class="size-4.5" />
                    Transcriber
                </NuxtLink>
                <nav class="ml-auto flex items-center gap-1 text-body-3">
                    <NuxtLink
                        v-for="l in NAV"
                        :key="l.to"
                        :to="l.to"
                        class="rounded-2xl px-3 py-1.5 text-title-3 text-text-muted hover:bg-surface-indent hover:text-text-default"
                        :class="isActive(l.to) && 'bg-surface-indent text-text-default!'"
                    >
                        {{ l.label }}
                    </NuxtLink>
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
