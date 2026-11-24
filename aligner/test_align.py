import numpy as np

from align import Word, align_words, group_words, viterbi

BLANK = 0
VOCAB = {"a": 1, "b": 2, "c": 3, "|": 4, "ø": 5}


def emissions(labels: list[int], n_classes: int = 6) -> np.ndarray:
    """Log-probs where frame t strongly prefers labels[t]."""
    lp = np.full((len(labels), n_classes), np.log(0.01))
    for t, c in enumerate(labels):
        lp[t, c] = np.log(0.95)
    return lp


def test_viterbi_follows_emissions():
    # frames: _ a a _ b _
    lp = emissions([0, 1, 1, 0, 2, 0])
    path = viterbi(lp, [1, 2], BLANK)
    assert path.tolist() == [-1, 0, 0, -1, 1, -1]


def test_viterbi_repeated_token_needs_blank():
    # "aa" must be a, blank, a
    lp = emissions([1, 0, 1])
    assert viterbi(lp, [1, 1], BLANK).tolist() == [0, -1, 1]
    assert viterbi(lp[:2], [1, 1], BLANK) is None


def test_align_words_times_and_unalignable():
    # 10 frames of 20 ms starting at 5.0 s: _ a a | b b | _ c _
    lp = emissions([0, 1, 1, 4, 2, 2, 4, 0, 3, 0])
    got = align_words(
        ["A,", "b", "42", "c."], lp, VOCAB, BLANK, 0.02, 5.0, delimiter=4
    )
    assert got[0] == (5.02, 5.06)
    assert got[1] == (5.08, 5.12)
    assert got[2] is None  # digits are not in the vocab
    assert got[3] == (5.16, 5.18)


def test_align_words_non_ascii():
    lp = emissions([0, 5, 0])
    assert align_words(["Ø"], lp, VOCAB, BLANK, 0.02, 0.0) == [(0.02, 0.04)]


def test_group_words_splits_on_pause_and_length():
    ws = [Word("a", 0, 1), Word("b", 1.2, 2), Word("c", 10, 11), Word("d", 11, 45)]
    assert group_words(ws) == [[0, 1], [2], [3]]
