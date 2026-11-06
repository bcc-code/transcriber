"""Unit tests for the VAD region planner. Run: uv run --no-project --with pytest pytest -q"""

from regions import MAX_REGION_SEC, plan_regions


def test_merges_close_regions():
    assert plan_regions([(1.0, 2.0), (2.3, 4.0)], 10) == [(0.8, 4.2)]


def test_keeps_far_regions_apart():
    assert plan_regions([(1.0, 2.0), (5.0, 6.0)], 10) == [(0.8, 2.2), (4.8, 6.2)]


def test_does_not_merge_past_max():
    got = plan_regions([(0.0, 20.0), (20.2, 35.0)], 40)
    assert len(got) == 2
    assert all(e - s <= MAX_REGION_SEC for s, e in got)


def test_hard_splits_long_region():
    got = plan_regions([(0.0, 70.0)], 70)
    assert len(got) == 3
    assert all(e - s <= MAX_REGION_SEC for s, e in got)
    assert got[0][0] == 0.0 and got[-1][1] == 70.0


def test_full_length_speech_stays_within_max_after_padding():
    got = plan_regions([(10.0, 10.0 + MAX_REGION_SEC - 0.4)], 100)
    assert len(got) == 1
    assert got[0][1] - got[0][0] <= MAX_REGION_SEC


def test_clamps_padding_to_audio():
    assert plan_regions([(0.05, 1.0)], 1.1) == [(0.0, 1.1)]


def test_empty():
    assert plan_regions([], 10) == []
