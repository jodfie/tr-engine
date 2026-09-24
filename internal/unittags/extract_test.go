package unittags

import (
	"reflect"
	"testing"
)

func TestExtract(t *testing.T) {
	type want struct{ key, display, pattern string }
	tests := []struct {
		name string
		text string
		want []want // nil = no candidates
	}{
		// ── Self-identification (should extract) ──
		{"dispatch addressee then caller", "County, Medic 12 on scene.",
			[]want{{"MEDIC 12", "Medic 12", PatternAddressee}}},
		{"lowercase no punctuation", "dispatch engine 5 responding",
			[]want{{"ENGINE 5", "Engine 5", PatternAddressee}}},
		{"place-name addressee + phonetic designator", "Butler County, 1 Paul 31, out at Main and 4th.",
			[]want{{"1 PAUL 31", "1 Paul 31", PatternAddressee}}},
		{"agency addressee", "Warren County Fire, Engine 71 on location, nothing showing.",
			[]want{{"ENGINE 71", "Engine 71", PatternAddressee}}},
		{"command addressee", "Command, Battalion Chief 2 on location.",
			[]want{{"BATTALION CHIEF 2", "Battalion Chief 2", PatternAddressee}}},
		{"bare reply to being called", "1 Paul 31.",
			[]want{{"1 PAUL 31", "1 Paul 31", PatternBare}}},
		{"bare reply with filler", "Uh, Medic 12.",
			[]want{{"MEDIC 12", "Medic 12", PatternBare}}},
		{"called party then caller", "Engine 5, Medic 12.",
			[]want{{"MEDIC 12", "Medic 12", PatternUnitCall}}},
		{"caller to dispatch", "Medic 12 to County.",
			[]want{{"MEDIC 12", "Medic 12", PatternCallerTo}}},
		{"caller to unit", "P338 to P340, what's your location?",
			[]want{{"P338", "P338", PatternCallerTo}}},
		{"addressee from caller", "Command from Engine 5, we're in position.",
			[]want{{"ENGINE 5", "Engine 5", PatternFrom}}},
		{"unit from unit", "Medic 14 from Medic 12.",
			[]want{{"MEDIC 12", "Medic 12", PatternFrom}}},
		{"spaced letter code with status", "Uh, P 338 en route.",
			[]want{{"P338", "P338", PatternStatus}}},
		{"hyphenated code", "Control, P-338.",
			[]want{{"P338", "P338", PatternAddressee}}},
		{"spoken number and we're", "Medic twelve, we're on scene",
			[]want{{"MEDIC 12", "Medic 12", PatternStatus}}},
		{"spoken compound number", "Engine twenty-one responding.",
			[]want{{"ENGINE 21", "Engine 21", PatternStatus}}},
		{"digit-by-digit number", "Medic one two en route to Atrium",
			[]want{{"MEDIC 12", "Medic 12", PatternStatus}}},
		{"this is", "Yeah this is Rescue 2, we're clear.",
			[]want{{"RESCUE 2", "Rescue 2", PatternThisIs}}},
		{"joined letter code", "FF1 responding",
			[]want{{"FF1", "FF1", PatternStatus}}},
		{"joined digit-letter-digit code", "1P31, on scene",
			[]want{{"1P31", "1P31", PatternStatus}}},
		{"car designator", "Car 12 in service.",
			[]want{{"CAR 12", "Car 12", PatternStatus}}},
		{"unit designator", "Unit 338, on scene",
			[]want{{"UNIT 338", "Unit 338", PatternStatus}}},
		{"joined word designator", "Medic12 en route",
			[]want{{"MEDIC 12", "Medic 12", PatternStatus}}},
		{"possessive status", "Medic 12's on scene",
			[]want{{"MEDIC 12", "Medic 12", PatternStatus}}},
		{"apco phonetic", "Adam 12 10-8",
			[]want{{"ADAM 12", "Adam 12", PatternStatus}}},
		{"copies", "Truck 3 copies.",
			[]want{{"TRUCK 3", "Truck 3", PatternStatus}}},
		{"closing over", "County, Medic 4, over.",
			[]want{{"MEDIC 4", "Medic 4", PatternAddressee}}},
		{"em dash pause", "Dispatch — Engine 7 — on scene",
			[]want{{"ENGINE 7", "Engine 7", PatternAddressee}}},
		{"spoken zero in a digit run", "County, Engine one oh five responding",
			[]want{{"ENGINE 105", "Engine 105", PatternAddressee}}},
		{"hyphenated spoken zero", "Engine one-oh-five on scene",
			[]want{{"ENGINE 105", "Engine 105", PatternStatus}}},
		{"letter o as zero", "Medic one o two en route",
			[]want{{"MEDIC 102", "Medic 102", PatternStatus}}},
		{"addressee then this is", "County, this is Medic 12, on scene.",
			[]want{{"MEDIC 12", "Medic 12", PatternThisIs}}},

		// ── Mentions / noise (must not extract) ──
		{"dispatcher addressing unit", "Medic 12, respond to 1234 Main Street for a fall.", nil},
		{"dispatcher paging multiple units", "Medic 12, Engine 5, Ladder 3, respond to a structure fire at 400 Oak.", nil},
		{"addressee then several units", "County, Medic 12 and Engine 5 on scene", nil},
		{"dispatcher echo after copy", "Copy, Medic 12 on scene at 14:32.", nil},
		{"quantity after number", "Engine 5 minutes out.", nil},
		{"addressee then quantity", "County, 2 minutes out", nil},
		{"ambiguous split number", "Medic 1 2 on scene", nil},
		{"ambiguous spoken number", "Medic one twenty on scene", nil},
		{"trailing spoken zero is not a digit", "County, Medic one oh on scene", nil},
		{"digit then spoken zero", "County, Engine 1 oh 5 responding", nil},
		{"teen then spoken zero", "Engine ten oh five on scene", nil},
		{"leading oh is not a number", "County, Engine oh five on scene", nil},
		{"interstate", "I-75 northbound at mile marker 32", nil},
		{"state route", "SR 4 and Main", nil},
		{"ten code alone", "10-4.", nil},
		{"station is a location", "Respond to Station 5.", nil},
		{"article is not a code", "A 12 year old male, conscious and breathing.", nil},
		{"channel", "Switch to TAC 2.", nil},
		{"go ahead is the dispatcher's reply", "Medic 12, go ahead.", nil},
		{"unit from dispatch is the dispatcher", "Engine 5 from County.", nil},
		{"dispatcher identifies itself, then calls a unit", "This is County, Medic 12 respond", nil},
		{"dispatcher self-id with a place name", "This is Butler County, Medic 12, respond to 400 Oak.", nil},
		{"function word before the dispatch word", "It's County, Engine 5, go ahead.", nil},
		{"from mid-sentence is not a call", "Patient was transferred from Medic 12.", nil},
		{"code three", "Code 3 to the hospital", nil},
		{"truck is blocking", "Truck 2 is blocking the road", nil},
		{"pure number self id", "338 on scene", nil},
		{"address", "1234 Main Street, apartment 5", nil},
		{"possessive is not this-is", "This is Medic 12's area.", nil},
		{"called party with message", "Engine 5, Medic 12, can you bring the stair chair", nil},
		{"vowel-less english word", "I'll be back by 5.", nil},
		{"time", "At 1430 hours", nil},
		{"empty", "", nil},
		{"punctuation only", "...", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.text)
			var gotW []want
			for _, c := range got {
				gotW = append(gotW, want{c.Key, c.Display, c.Pattern})
			}
			if !reflect.DeepEqual(gotW, tt.want) {
				t.Errorf("Extract(%q) = %+v, want %+v", tt.text, gotW, tt.want)
			}
		})
	}
}

// TestExtractNoisyTraffic runs Whisper-style transcripts of real radio
// patterns: missing/odd punctuation, fillers, run-on sentences, and dispatcher
// traffic that merely mentions units. Positives are the speaker's own ID.
func TestExtractNoisyTraffic(t *testing.T) {
	tests := []struct {
		text string
		want string // expected Key, "" = no candidate
	}{
		// Unit self-identification
		{"Butler County Medic 12 show us on scene", "MEDIC 12"},
		{"Medic 12 show us en route to Fort Hamilton", "MEDIC 12"},
		{"county uh medic 12 on scene", "MEDIC 12"},
		{"County, uh, Medic 12, we'll be en route.", "MEDIC 12"},
		{"Warren County, 1 Paul 31, I'll be out at the Kroger on Tylersville.", "1 PAUL 31"},
		// A bare place name is not a recognized addressee; accepting "<word>, <ID>
		// <status>" would also accept dispatcher echoes ("Okay, Medic 12 on scene").
		{"Warren, 1 Paul 31, I'll be out at the Kroger on Tylersville.", ""},
		{"Okay, Medic 12 on scene.", ""},
		{"Dispatch, Engine 71, we're on location, two-story residential, nothing showing.", "ENGINE 71"},
		{"Engine 71 on location, nothing showing, we'll be investigating.", "ENGINE 71"},
		{"Fire Dispatch Truck 3 responding", "TRUCK 3"},
		{"Medic 12 to Butler County.", "MEDIC 12"},
		{"um, P338, uh, en route", "P338"},
		{"Unit 338 we're clear, back in service.", "UNIT 338"},
		{"County, Battalion Chief 2, I'll be staging at Station 5.", "BATTALION CHIEF 2"},
		{"Yeah this is Rescue 2 we're clear", "RESCUE 2"},
		{"Adam twelve, show me ten eight.", ""}, // "ten eight" spoken is not a status phrase — conservative miss

		// Dispatcher / other speakers: mentions only
		{"Medic 12, show you on scene, 14:32.", ""},
		{"Medic 12 copy.", ""},
		{"Medic 12, Butler County.", ""},
		{"Engine 71, Medic 12, Truck 3, respond to a reported structure fire, 400 Oak Street, cross of Main.", ""},
		{"County copies Medic 12 on scene.", ""},
		{"Units responding to 400 Oak, Engine 71 has command.", ""},
		{"We'll need Medic 12 and Engine 5 at the staging area.", ""},
		{"County, Medic 12, uh, Engine 5 on scene", ""},
		{"County, Medic 12 and uh Engine 5 on scene", ""},
		{"Engine 5 you're clear to return.", ""},
		{"Thank you.", ""},
		{"10-4, 10-4.", ""},
		{"Northbound I-75 at mile marker 22, two vehicles.", ""},
		{"It'll be 1234 Main Street, apartment 2B, 2B.", ""},
		{"He's about 5 foot 10, last seen heading east on Main.", ""},
		{"Plate is Adam Boy Charles 1 2 3 4.", ""},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := Extract(tt.text)
			switch {
			case tt.want == "" && len(got) > 0:
				t.Errorf("Extract(%q) = %+v, want none", tt.text, got)
			case tt.want != "" && (len(got) != 1 || got[0].Key != tt.want):
				t.Errorf("Extract(%q) = %+v, want [%s]", tt.text, got, tt.want)
			}
		})
	}
}

func TestDesignatorKeys(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"BCFD MEDIC 12", []string{"MEDIC 12"}},
		{"M12", []string{"M12"}},
		{"1P31", []string{"1P31"}},
		{"P-338", []string{"P338"}},
		{"Engine 5 / Medic 5", []string{"ENGINE 5", "MEDIC 5"}},
		{"FIRE TAC 2", nil},
		{"BC FIRE DISP", nil},
		{"Dispatch", nil},
	}
	for _, tt := range tests {
		got := DesignatorKeys(tt.text)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("DesignatorKeys(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestMatchesTag(t *testing.T) {
	tests := []struct {
		key, tag string
		want     bool
	}{
		{"MEDIC 12", "Medic 12", true},
		{"MEDIC 12", "MEDIC-12", true},
		{"MEDIC 12", "BCFD Medic 12", true},
		{"P338", "P 338", true},
		{"P338", "p338", true},
		{"MEDIC 12", "Medic 1", false},
		{"MEDIC 12", "", false},
		{"ENGINE 5", "Dispatch", false},
		{"1 PAUL 31", "1 Paul 31", true},
		// Phonetic words compare equal to their letter, both ways.
		{"1 PAUL 31", "1P31", true},
		{"1 PAUL 31", "1-P-31", true},
		{"2 ADAM 12", "2A12", true},
		{"ADAM 12", "A12", true},
		{"ADAM 12", "BCSO A12", true},
		{"1P31", "1 Paul 31", true},
		{"1 PAUL 31", "1P32", false},
		{"1 PAUL 31", "2P31", false},
		{"MEDIC 12", "M12", false}, // apparatus words are not phonetic letters
	}
	for _, tt := range tests {
		if got := MatchesTag(tt.key, tt.tag); got != tt.want {
			t.Errorf("MatchesTag(%q, %q) = %v, want %v", tt.key, tt.tag, got, tt.want)
		}
	}
}
