"""CTC forced alignment of known text against wav2vec2 emissions.

Pure numpy so it can be unit-tested without torch. The model side lives in
server.py.
"""

from __future__ import annotations

import unicodedata
from dataclasses import dataclass

import numpy as np


@dataclass
class Word:
    text: str
    start: float
    end: float


def normalize_word(word: str, vocab: dict[str, int]) -> list[int]:
    """Token ids for the characters of `word` that the model can emit.

    Lowercases, keeps letters the vocab knows (æ, ø, å, ä, ü, ...), drops
    punctuation and anything else (digits are usually not in a CTC vocab).
    """
    ids = []
    for ch in unicodedata.normalize("NFC", word.lower()):
        if ch in vocab:
            ids.append(vocab[ch])
    return ids


def viterbi(log_probs: np.ndarray, tokens: list[int], blank: int) -> np.ndarray | None:
    """Most likely CTC path for `tokens` through `log_probs` (T x C).

    Returns, for every frame, the index into `tokens` it is aligned to, or -1
    for a blank frame. None when the text cannot fit in T frames.
    """
    T = log_probs.shape[0]
    N = len(tokens)
    if N == 0:
        return np.full(T, -1, dtype=np.int64)
    # Extended label sequence: blank, t0, blank, t1, ..., t(N-1), blank.
    S = 2 * N + 1
    ext = np.full(S, blank, dtype=np.int64)
    ext[1::2] = tokens
    # A skip over the blank between two tokens is only allowed when they differ.
    can_skip = np.zeros(S, dtype=bool)
    can_skip[3::2] = ext[3::2] != ext[1:-2:2]

    # Frames needed: one per token plus one blank between equal neighbours.
    min_frames = N + int(np.sum(~can_skip[3::2]))
    if T < min_frames:
        return None

    neg = -np.inf
    dp = np.full(S, neg)
    dp[0] = log_probs[0, ext[0]]
    dp[1] = log_probs[0, ext[1]]
    back = np.zeros((T, S), dtype=np.int8)  # 0 stay, 1 from s-1, 2 from s-2
    emit = log_probs[:, ext]  # T x S
    for t in range(1, T):
        stay = dp
        step = np.concatenate(([neg], dp[:-1]))
        skip = np.where(can_skip, np.concatenate(([neg, neg], dp[:-2])), neg)
        cand = np.stack((stay, step, skip))
        choice = np.argmax(cand, axis=0)
        dp = cand[choice, np.arange(S)] + emit[t]
        back[t] = choice

    s = S - 1 if dp[S - 1] >= dp[S - 2] else S - 2
    if not np.isfinite(dp[s]):
        return None
    path = np.empty(T, dtype=np.int64)
    for t in range(T - 1, -1, -1):
        path[t] = (s - 1) // 2 if s % 2 == 1 else -1
        s -= int(back[t, s])
    return path


def align_words(
    words: list[str],
    log_probs: np.ndarray,
    vocab: dict[str, int],
    blank: int,
    frame_sec: float,
    offset: float,
    delimiter: int | None = None,
) -> list[tuple[float, float] | None]:
    """Start/end in seconds for each word, or None when it has no alignable
    characters (e.g. a bare number) or the text does not fit the audio.

    `delimiter` is the model's word separator token ("|"); putting it between
    words matches how wav2vec2 CTC models are trained.
    """
    tokens: list[int] = []
    owner: list[int] = []  # word index per token, -1 for delimiters
    for wi, w in enumerate(words):
        ids = normalize_word(w, vocab)
        if not ids:
            continue
        if delimiter is not None and tokens:
            tokens.append(delimiter)
            owner.append(-1)
        tokens.extend(ids)
        owner.extend([wi] * len(ids))

    out: list[tuple[float, float] | None] = [None] * len(words)
    path = viterbi(log_probs, tokens, blank)
    if path is None:
        return out

    first: dict[int, int] = {}
    last: dict[int, int] = {}
    for t, ti in enumerate(path):
        if ti < 0:
            continue
        wi = owner[ti]
        if wi < 0:
            continue
        first.setdefault(wi, t)
        last[wi] = t
    for wi in first:
        start = offset + first[wi] * frame_sec
        end = offset + (last[wi] + 1) * frame_sec
        out[wi] = (round(start, 3), round(end, 3))
    return out


def group_words(
    words: list[Word], max_gap: float = 1.5, max_len: float = 30.0
) -> list[list[int]]:
    """Split a segment's words into windows to align separately.

    A long pause (where VAD cut silence or music) or a long run starts a new
    window, so no window carries minutes of non-speech and each stays small
    enough for the model.
    """
    groups: list[list[int]] = []
    cur: list[int] = []
    for i, w in enumerate(words):
        if cur:
            prev = words[cur[-1]]
            if w.start - prev.end > max_gap or w.end - words[cur[0]].start > max_len:
                groups.append(cur)
                cur = []
        cur.append(i)
    if cur:
        groups.append(cur)
    return groups
