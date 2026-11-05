"""Turn VAD speech timestamps into model-sized regions.

No heavy dependencies, so it unit-tests without torch.
"""

from __future__ import annotations

import os

# Upstream rejects inputs > 40 s; stay well clear and keep subtitle cues short.
MAX_REGION_SEC = float(os.environ.get("OMNI_MAX_REGION_SEC", "30"))
# Adjacent speech regions closer than this are merged into one segment.
MERGE_GAP_SEC = 0.5
# Padding kept around each region so word edges aren't clipped.
PAD_SEC = 0.2
# Longest unpadded speech span; padding then brings it up to MAX_REGION_SEC.
MAX_SPEECH_SEC = MAX_REGION_SEC - 2 * PAD_SEC


def plan_regions(speech: list[tuple[float, float]], total: float) -> list[tuple[float, float]]:
    """Merge close VAD regions up to MAX_REGION_SEC, hard-split longer ones, pad edges.

    `speech` is a sorted list of (start, end) seconds; `total` is the audio
    length. Every returned region is at most MAX_REGION_SEC long.
    """
    merged: list[list[float]] = []
    for start, end in speech:
        if merged and start - merged[-1][1] < MERGE_GAP_SEC and end - merged[-1][0] <= MAX_SPEECH_SEC:
            merged[-1][1] = end
        else:
            merged.append([start, end])

    out: list[tuple[float, float]] = []
    for start, end in merged:
        # VAD is asked for spans <= MAX_SPEECH_SEC, but split defensively.
        while end - start > MAX_SPEECH_SEC:
            out.append((start, start + MAX_SPEECH_SEC))
            start += MAX_SPEECH_SEC
        out.append((start, end))

    padded: list[tuple[float, float]] = []
    for start, end in out:
        start = max(0.0, start - PAD_SEC)
        end = min(total, end + PAD_SEC)
        if end - start > 0.05:
            padded.append((round(start, 3), round(end, 3)))
    return padded
