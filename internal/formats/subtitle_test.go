package formats

import (
	"reflect"
	"testing"

	"transcriber/internal/transcriber"
)

func words(specs ...wordSpec) []transcriber.Word {
	out := make([]transcriber.Word, 0, len(specs))
	for _, s := range specs {
		out = append(out, transcriber.Word{Text: s.text, Start: s.start, End: s.end})
	}
	return out
}

type wordSpec struct {
	text       string
	start, end float64
}

func TestBuildCues_LineWrapWithinCue(t *testing.T) {
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{{
		Start: 0, End: 5,
		Words: words(
			wordSpec{"This", 0, 0.3},
			wordSpec{"is", 0.3, 0.5},
			wordSpec{"a", 0.5, 0.6},
			wordSpec{"test", 0.6, 1.0},
			wordSpec{"of", 1.0, 1.2},
			wordSpec{"the", 1.2, 1.4},
			wordSpec{"transcription", 1.4, 2.8},
			wordSpec{"system.", 2.8, 5.0},
		),
	}}}

	cues := BuildCues(tr, SubtitleOptions{})
	want := []Cue{{
		Start: 0, End: 5.0 + cueTailPad,
		Lines: []string{"This is a test of the", "transcription system."},
	}}
	if !reflect.DeepEqual(cues, want) {
		t.Errorf("got %+v, want %+v", cues, want)
	}
}

func TestBuildCues_FlushOnMaxLines(t *testing.T) {
	// Three lines forces a second cue at maxLines=2.
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{{
		Start: 0, End: 9,
		Words: words(
			wordSpec{"aaaaaaaa", 0, 1},
			wordSpec{"bbbbbbbb", 1, 2},
			wordSpec{"cccccccc", 2, 3},
			wordSpec{"dddddddd", 3, 4},
			wordSpec{"eeeeeeee", 4, 5},
			wordSpec{"ffffffff", 5, 6},
		),
	}}}

	cues := BuildCues(tr, SubtitleOptions{MaxCharsPerLine: 8, MaxLinesPerCue: 2})
	want := []Cue{
		{Start: 0, End: 2, Lines: []string{"aaaaaaaa", "bbbbbbbb"}},
		{Start: 2, End: 4, Lines: []string{"cccccccc", "dddddddd"}},
		{Start: 4, End: 6 + cueTailPad, Lines: []string{"eeeeeeee", "ffffffff"}},
	}
	if !reflect.DeepEqual(cues, want) {
		t.Errorf("got %+v, want %+v", cues, want)
	}
}

func TestBuildCues_NoWordsFallsBackToSegment(t *testing.T) {
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{{
		Start: 1.0, End: 4.5,
		Text: "Hello there friend.",
	}}}

	cues := BuildCues(tr, SubtitleOptions{})
	want := []Cue{{Start: 1.0, End: 4.5 + cueTailPad, Lines: []string{"Hello there friend."}}}
	if !reflect.DeepEqual(cues, want) {
		t.Errorf("got %+v, want %+v", cues, want)
	}
}

func TestBuildCues_AvoidsOrphanWithClauseBreak(t *testing.T) {
	// Without the clause break, "hhh" would dangle as a single-word cue.
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{{
		Start: 0, End: 8,
		Words: words(
			wordSpec{"aaa", 0, 1},
			wordSpec{"bbb", 1, 2},
			wordSpec{"ccc,", 2, 3},
			wordSpec{"ddd", 3, 4},
			wordSpec{"eee", 4, 5},
			wordSpec{"fff", 5, 6},
			wordSpec{"ggg", 6, 7},
			wordSpec{"hhh", 7, 8},
		),
	}}}

	cues := BuildCues(tr, SubtitleOptions{MaxCharsPerLine: 13, MaxLinesPerCue: 2})
	want := []Cue{
		{Start: 0, End: 3, Lines: []string{"aaa bbb ccc,"}},
		{Start: 3, End: 8 + cueTailPad, Lines: []string{"ddd eee", "fff ggg hhh"}},
	}
	if !reflect.DeepEqual(cues, want) {
		t.Errorf("got %+v, want %+v", cues, want)
	}
}

func TestBuildCues_BalancedTwoLineSplit(t *testing.T) {
	// Greedy fill would leave a short tail line; balanced wrap distributes evenly.
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{{
		Start: 0, End: 6,
		Words: words(
			wordSpec{"Hello", 0, 1},
			wordSpec{"world", 1, 2},
			wordSpec{"this", 2, 3},
			wordSpec{"is", 3, 4},
			wordSpec{"a", 4, 4.3},
			wordSpec{"farewell.", 4.3, 6},
		),
	}}}

	cues := BuildCues(tr, SubtitleOptions{MaxCharsPerLine: 20, MaxLinesPerCue: 2})
	want := []Cue{{
		Start: 0, End: 6 + cueTailPad,
		Lines: []string{"Hello world this", "is a farewell."},
	}}
	if !reflect.DeepEqual(cues, want) {
		t.Errorf("got %+v, want %+v", cues, want)
	}
}

func TestBuildCues_BreaksAtSentenceEndNotAbbreviation(t *testing.T) {
	// "2016." is a real sentence end; "J." is an abbreviation. The cue
	// boundary should land at the former, not the latter.
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{{
		Start: 0, End: 10,
		Words: words(
			wordSpec{"Det", 0, 0.2},
			wordSpec{"er", 0.2, 0.4},
			wordSpec{"blitt", 0.4, 0.8},
			wordSpec{"torsdag", 0.8, 1.2},
			wordSpec{"15.12.2016.", 1.2, 3.0},
			wordSpec{"Aksel", 3.0, 3.3},
			wordSpec{"J.", 3.3, 3.5},
			wordSpec{"Smith", 3.5, 3.9},
			wordSpec{"skriver", 3.9, 4.4},
			wordSpec{"i", 4.4, 4.5},
			wordSpec{"Skjulte", 4.5, 4.9},
			wordSpec{"skatter", 4.9, 5.4},
			wordSpec{"i", 5.4, 5.5},
			wordSpec{"november", 5.5, 6.0},
			wordSpec{"1978:", 6.0, 6.5},
		),
	}}}

	cues := BuildCues(tr, SubtitleOptions{})
	if len(cues) < 2 {
		t.Fatalf("got %d cues, want at least 2: %+v", len(cues), cues)
	}
	wantFirst := "Det er blitt torsdag 15.12.2016."
	if cues[0].Lines[0] != wantFirst {
		t.Errorf("cue 0 line 0 = %q, want %q (cue contents: %+v)", cues[0].Lines[0], wantFirst, cues[0].Lines)
	}
}

func TestBuildCues_LongWordFitsOnOwnLine(t *testing.T) {
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{{
		Start: 0, End: 2,
		Words: words(
			wordSpec{"supercalifragilisticexpialidocious", 0, 1.5},
			wordSpec{"ok.", 1.5, 2.0},
		),
	}}}

	cues := BuildCues(tr, SubtitleOptions{MaxCharsPerLine: 10, MaxLinesPerCue: 2})
	want := []Cue{{
		Start: 0, End: 2.0 + cueTailPad,
		Lines: []string{"supercalifragilisticexpialidocious", "ok."},
	}}
	if !reflect.DeepEqual(cues, want) {
		t.Errorf("got %+v, want %+v", cues, want)
	}
}

func TestBuildCues_ExtendsEndUntilNextCue(t *testing.T) {
	tr := &transcriber.Transcription{Segments: []transcriber.Segment{
		{Start: 0, End: 1.0, Text: "Big gap after."},
		{Start: 3.0, End: 4.0, Text: "Small gap after."},
		{Start: 4.3, End: 5.0, Text: "Touching."},
		{Start: 5.0, End: 6.0, Text: "Last one."},
	}}

	cues := BuildCues(tr, SubtitleOptions{})
	got := make([]float64, len(cues))
	for i, c := range cues {
		got[i] = c.End
	}
	want := []float64{1.8, 4.3, 5.0, 6.8}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ends = %v, want %v", got, want)
	}
}
