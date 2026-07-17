package subtitle

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAndWriteSRT(t *testing.T) {
	in := "1\r\n00:00:01,000 --> 00:00:02,500\r\nHello!\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\n<i>How are\r\nyou?</i>\r\n"
	cues, err := ParseSRT(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := cues[1].Text; got != "<i>How are\nyou?</i>" {
		t.Fatalf("text = %q", got)
	}
	var out bytes.Buffer
	if err := WriteSRT(&out, cues); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "00:00:01,000 --> 00:00:02,500\nHello!") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestParseRejectsInvalidTiming(t *testing.T) {
	_, err := ParseSRT(strings.NewReader("1\nnot a time\nhello\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeForTranslationRemovesASSDrawingAndMergesAnimation(t *testing.T) {
	cues := []Cue{
		{Index: 1, Timing: "00:00:01,000 --> 00:00:01,040", Text: `<font color="#111"><b>{\an8}AGONIA</b></font>`},
		{Index: 2, Timing: "00:00:01,040 --> 00:00:01,080", Text: `<font color="#222"><b>{\an8}AGONIA</b></font>`},
		{Index: 3, Timing: "00:00:01,080 --> 00:00:02,000", Text: `{\an7}m -174 -165 b -176 -167 -178 -169 -179 -172 b -182 -178 -182 -184 -181 -190 b -180 -194 -178 -200 -175 -204`},
		{Index: 4, Timing: "00:00:02,100 --> 00:00:03,000", Text: `<font face="Cabin"><b>Hello, world!</b></font>`},
	}
	got, stats := NormalizeForTranslation(cues)
	if len(got) != 2 {
		t.Fatalf("normalized = %#v, stats=%#v", got, stats)
	}
	if got[0].Text != "AGONIA" || got[0].Timing != "00:00:01,000 --> 00:00:01,080" {
		t.Fatalf("merged animation = %#v", got[0])
	}
	if got[1].Text != "Hello, world!" || got[1].Index != 2 {
		t.Fatalf("dialogue = %#v", got[1])
	}
	if stats.Merged != 1 || stats.RemovedDrawings != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestNormalizeForTranslationDoesNotMergeDistantRepeatedDialogue(t *testing.T) {
	cues := []Cue{
		{Index: 1, Timing: "00:00:01,000 --> 00:00:02,000", Text: "Sim."},
		{Index: 2, Timing: "00:00:04,000 --> 00:00:05,000", Text: "Sim."},
	}
	got, _ := NormalizeForTranslation(cues)
	if len(got) != 2 {
		t.Fatalf("normalized = %#v", got)
	}
}
