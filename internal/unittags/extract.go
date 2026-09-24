// Package unittags finds radio unit self-identifications ("Medic 12 on scene",
// "County, P338") in attributed transcriptions and queues them as unit alpha
// tag suggestions for human review. Nothing in this package modifies the units
// table — suggestions are only applied through the explicit approve API.
package unittags

import (
	"strings"
	"unicode"
)

// Pattern names recorded with each piece of evidence so reviewers can see why
// a candidate was extracted.
const (
	PatternAddressee = "addressee_caller" // "County, Medic 12 on scene" — caller follows a dispatch addressee
	PatternUnitCall  = "unit_caller"      // "Engine 5, Medic 12." — "<called>, <caller>" as the whole transmission
	PatternCallerTo  = "caller_to"        // "Medic 12 to County"
	PatternFrom      = "from_caller"      // "Command from Engine 5", "Medic 14 from Medic 12"
	PatternThisIs    = "this_is"          // "this is Rescue 2"
	PatternStatus    = "status"           // "Engine 5 on scene", "P338 en route"
	PatternBare      = "bare"             // transmission is only "<ID>" (answering a call)
)

// Candidate is a unit designator the speaker used to identify themselves.
type Candidate struct {
	Key     string // normalized comparison key, e.g. "MEDIC 12", "P338", "1 PAUL 31"
	Display string // human-readable form, e.g. "Medic 12", "P338", "1 Paul 31"
	Pattern string // one of the Pattern* constants
}

// Extract returns the self-identification candidates found in one unit's
// transmission text. It is deliberately conservative: a designator only counts
// when its position in the transmission marks it as the speaker's own ID
// (radio protocol is "<called party>, <caller>"), never when it is merely
// mentioned ("Medic 12, respond to ..." is the dispatcher addressing Medic 12).
// Results are de-duplicated by Key, in order of appearance.
func Extract(text string) []Candidate {
	toks := tokenize(text)
	var out []Candidate
	seen := make(map[string]bool)
	add := func(d designator, pattern string) {
		if seen[d.key] {
			return
		}
		seen[d.key] = true
		out = append(out, Candidate{Key: d.key, Display: d.display, Pattern: pattern})
	}

	start := skipLead(toks, 0)
	if start < len(toks) {
		if next, ok := parseAddressee(toks, start); ok {
			// "County, Medic 12 ..." / "Dispatch Engine 5 responding" / "County, uh, Medic 12"
			// / "Command from Engine 5"
			k := skipLead(toks, next)
			pattern := PatternAddressee
			if isWordAt(toks, k, "from") {
				k, pattern = skipLead(toks, k+1), PatternFrom
			}
			if d, ok := parseDesignatorAt(toks, k); ok && !anotherUnitFollows(toks, k+d.n) {
				add(d, pattern)
			}
		} else if a, ok := parseDesignatorAt(toks, start); ok {
			afterA := start + a.n
			k := skipLead(toks, afterA)
			switch {
			case atEnd(toks, afterA):
				// "1 Paul 31." — answering a call with only its own ID.
				add(a, PatternBare)
			case isWordAt(toks, afterA, "to") && (addresseeAt(toks, afterA+1) || designatorAt(toks, afterA+1)):
				// "Medic 12 to County"
				add(a, PatternCallerTo)
			case isWordAt(toks, afterA, "from"):
				// "Medic 14 from Medic 12" — the caller follows "from". "Engine 5
				// from County" is the dispatcher speaking and yields nothing.
				if x, ok := parseDesignatorAt(toks, afterA+1); ok && x.key != a.key {
					add(x, PatternFrom)
				}
			case statusAt(toks, k):
				// "Engine 5, on scene" / "Medic 12 we're en route"
				add(a, PatternStatus)
			default:
				// "Engine 5, Medic 12." — the caller follows the called party.
				// Only when the two IDs are the entire transmission; with more
				// content it is usually a dispatcher addressing several units.
				if x, ok := parseDesignatorAt(toks, k); ok && x.key != a.key && atEnd(toks, k+x.n) {
					add(x, PatternUnitCall)
				}
			}
		}
	}

	// "this is Rescue 2" anywhere in the transmission.
	for i := 0; i+2 < len(toks); i++ {
		if !isWordAt(toks, i, "this") || !isWordAt(toks, i+1, "is") {
			continue
		}
		d, ok := parseDesignatorAt(toks, i+2)
		if !ok {
			continue
		}
		after := i + 2 + d.n
		if atEnd(toks, after) || isBreakAt(toks, after) || isWordAt(toks, after, "to") || statusAt(toks, after) {
			add(d, PatternThisIs)
		}
	}
	return out
}

// DesignatorKeys returns the keys of every unit designator appearing anywhere
// in text (no positional rules). Used to build exclusion sets from talkgroup
// names and a unit's current alpha tag.
func DesignatorKeys(text string) []string {
	toks := tokenize(text)
	var keys []string
	for i := 0; i < len(toks); i++ {
		if d, ok := parseDesignatorAt(toks, i); ok {
			keys = append(keys, d.key)
			i += d.n - 1
		}
	}
	return keys
}

// MatchesTag reports whether key refers to the same designator as an existing
// alpha tag — either the whole tag (ignoring case, spaces and hyphens) or a
// designator contained in it ("BCFD Medic 12" matches "MEDIC 12"). Phonetic
// words compare equal to their letter, so "1 PAUL 31" matches "1P31" and
// "ADAM 12" matches "A12" (and the reverse).
func MatchesTag(key, tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" || key == "" {
		return false
	}
	want := letterForm(key)
	if compact(tag) == compact(key) || letterForm(tag) == want {
		return true
	}
	for _, k := range DesignatorKeys(tag) {
		if k == key || letterForm(k) == want {
			return true
		}
	}
	return false
}

// letterForm is the compact uppercase form of text with each APCO/NATO
// phonetic word reduced to its letter: "1 Paul 31" and "1-P-31" → "1P31",
// "Adam 12" → "A12". Other words are kept whole ("Medic 12" → "MEDIC12").
func letterForm(text string) string {
	var b strings.Builder
	for _, t := range tokenize(text) {
		for _, part := range strings.Split(t.s, "-") {
			if phoneticWords[part] {
				part = part[:1]
			}
			b.WriteString(strings.ToUpper(part))
		}
	}
	return b.String()
}

func compact(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r == ' ' || r == '-' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ── Tokenizer ────────────────────────────────────────────────────────

type token struct {
	s   string // lowercase word; empty for breaks
	brk bool   // punctuation boundary (, . ; : ! ? or a dash used as a pause)
}

// tokenize lowercases text and splits it into word tokens and punctuation
// breaks. Hyphenated and joined forms are normalized so "P-338", "Medic12",
// "on-scene" and "twenty-one" parse the same as their spaced variants.
func tokenize(text string) []token {
	var toks []token
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			toks = append(toks, splitWord(b.String())...)
			b.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '’':
			b.WriteRune('\'')
		case r == '-':
			b.WriteRune('-')
		case r == ',' || r == '.' || r == ';' || r == ':' || r == '!' || r == '?' || r == '–' || r == '—':
			flush()
			toks = append(toks, token{brk: true})
		default:
			flush()
		}
	}
	flush()
	return toks
}

func splitWord(w string) []token {
	w = strings.Trim(w, "-'")
	if w == "" {
		// A free-standing hyphen is a spoken pause.
		return []token{{brk: true}}
	}

	// "12's on scene" → "12 is on scene"; other possessives/contractions just
	// lose the apostrophe ("we're" → "were").
	if strings.HasSuffix(w, "'s") {
		base := w[:len(w)-2]
		if isDigits(base) {
			return []token{{s: base}, {s: "is"}}
		}
		w = base
	}
	w = strings.ReplaceAll(w, "'", "")
	if w == "" {
		return nil
	}

	if strings.Contains(w, "-") {
		parts := strings.FieldsFunc(w, func(r rune) bool { return r == '-' })
		switch {
		case allParts(parts, isLetters):
			// "on-scene", "twenty-one", "en-route"
			out := make([]token, len(parts))
			for i, p := range parts {
				out[i] = token{s: p}
			}
			return out
		case len(parts) == 2 && isLetters(parts[0]) && isDigits(parts[1]):
			if prefixWords[parts[0]] || phoneticWords[parts[0]] {
				return []token{{s: parts[0]}, {s: parts[1]}} // "medic-12"
			}
			w = parts[0] + parts[1] // "p-338" → "p338"
		default:
			return []token{{s: w}} // "10-4", "10-97": kept whole, never a designator number
		}
	}

	// "medic12" → "medic 12" when the letters are a known designator word.
	if i := strings.IndexFunc(w, unicode.IsDigit); i > 0 && isDigits(w[i:]) {
		letters := w[:i]
		if isLetters(letters) && (prefixWords[letters] || phoneticWords[letters]) {
			return []token{{s: letters}, {s: w[i:]}}
		}
	}
	return []token{{s: w}}
}

// ── Designator grammar ───────────────────────────────────────────────

type designator struct {
	key     string
	display string
	n       int // tokens consumed
}

// parseDesignatorAt recognizes a unit designator starting at toks[i]:
//
//	1 Paul 31       digits + APCO/NATO phonetic word + number
//	Battalion Chief 2  two-word apparatus/rank prefix + number
//	Medic 12 / Engine five / Adam 12   apparatus/rank/phonetic word + number
//	P 338 / FF 1    short letter code + digits
//	P338 / FF1 / 1P31  joined alphanumeric code
//
// Pure numbers are never designators (too often addresses, times or
// 10-codes), and a designator whose number is followed by a quantity word
// ("Engine 5 minutes out") or another number ("Medic 1 2") is rejected as
// ambiguous.
func parseDesignatorAt(toks []token, i int) (designator, bool) {
	if i >= len(toks) || toks[i].brk {
		return designator{}, false
	}
	w := toks[i].s

	// 1 Paul 31
	if isDigits(w) && len(w) <= 2 && i+2 < len(toks) && phoneticWords[toks[i+1].s] {
		if num, n, ok := numberAt(toks, i+2); ok && followOK(toks, i+2+n) {
			display := w + " " + titleWord(toks[i+1].s) + " " + num
			return designator{key: strings.ToUpper(display), display: display, n: 2 + n}, true
		}
	}

	// Battalion Chief 2
	if i+1 < len(toks) && !toks[i+1].brk {
		if twoWordPrefixes[w+" "+toks[i+1].s] {
			if num, n, ok := numberAt(toks, i+2); ok && followOK(toks, i+2+n) {
				display := titleWord(w) + " " + titleWord(toks[i+1].s) + " " + num
				return designator{key: strings.ToUpper(display), display: display, n: 2 + n}, true
			}
		}
	}

	// Medic 12, Engine five, Adam 12
	if prefixWords[w] || phoneticWords[w] {
		if num, n, ok := numberAt(toks, i+1); ok && followOK(toks, i+1+n) {
			display := titleWord(w) + " " + num
			return designator{key: strings.ToUpper(display), display: display, n: 1 + n}, true
		}
		return designator{}, false
	}

	// P 338, FF 1 — spaced short letter code followed by digits.
	if isShortCodeLetters(w) && i+1 < len(toks) && !toks[i+1].brk {
		if d := toks[i+1].s; isDigits(d) && len(d) <= 4 && followOK(toks, i+2) {
			code := strings.ToUpper(w + d)
			return designator{key: code, display: code, n: 2}, true
		}
	}

	// P338, FF1, 1P31 — joined code in a single token.
	if isJoinedCode(w) && followOK(toks, i+1) {
		code := strings.ToUpper(w)
		return designator{key: code, display: code, n: 1}, true
	}
	return designator{}, false
}

func designatorAt(toks []token, i int) bool {
	_, ok := parseDesignatorAt(toks, i)
	return ok
}

// numberAt parses a unit number at toks[i]: 1-4 digits, or a spoken number
// ("twelve", "twenty one", "one two" → 12).
func numberAt(toks []token, i int) (string, int, bool) {
	if i >= len(toks) || toks[i].brk {
		return "", 0, false
	}
	w := toks[i].s
	if isDigits(w) {
		if len(w) > 4 {
			return "", 0, false
		}
		return w, 1, true
	}
	if v, ok := tensWords[w]; ok {
		if i+1 < len(toks) && !toks[i+1].brk {
			if o, ok := onesWords[toks[i+1].s]; ok && o != "0" {
				return string(rune('0'+v)) + o, 2, true
			}
		}
		return string(rune('0'+v)) + "0", 1, true
	}
	if v, ok := teenWords[w]; ok {
		return v, 1, true
	}
	if _, ok := onesWords[w]; ok {
		// Digit-by-digit ("one two" → 12, "one oh five" → 105), up to three
		// digits. A tens/teen word after it ("one twenty") is ambiguous;
		// followOK rejects it.
		var digits []string
		for i+len(digits) < len(toks) && len(digits) < 3 && !toks[i+len(digits)].brk {
			s := toks[i+len(digits)].s
			o, ok := onesWords[s]
			if !ok && len(digits) > 0 && spokenZeroWords[s] {
				o, ok = "0", true
			}
			if !ok {
				break
			}
			digits = append(digits, o)
		}
		// A trailing "oh" is not taken as a digit ("Medic one oh" could be
		// "Medic 1, oh ..."): leave it for followOK, which rejects the
		// designator instead of guessing.
		for len(digits) > 1 && spokenZeroWords[toks[i+len(digits)-1].s] {
			digits = digits[:len(digits)-1]
		}
		return strings.Join(digits, ""), len(digits), true
	}
	return "", 0, false
}

// followOK rejects designators whose number is immediately followed by a
// quantity word, another number, or a spoken zero ("Engine 1 oh 5" is not
// Engine 1).
func followOK(toks []token, i int) bool {
	if i >= len(toks) || toks[i].brk {
		return true
	}
	w := toks[i].s
	if quantityWords[w] || isNumberish(w) || spokenZeroWords[w] {
		return false
	}
	return true
}

func isNumberish(w string) bool {
	// Pure digits only: hyphenated 10-codes ("Adam 12 10-8") are not numbers.
	if isDigits(w) {
		return true
	}
	_, a := onesWords[w]
	_, b := teenWords[w]
	_, c := tensWords[w]
	return a || b || c || w == "hundred" || w == "thousand"
}

// isShortCodeLetters accepts a stand-alone 1-3 letter token that can prefix a
// unit number when spoken separately ("P 338", "FF 1"): a single letter other
// than a/i/o/x, or a vowel-less abbreviation, minus common non-unit prefixes
// (road, time, measurement and channel abbreviations).
func isShortCodeLetters(w string) bool {
	if !isLetters(w) || len(w) > 3 || codeExclusions[w] {
		return false
	}
	if len(w) == 1 {
		return w != "a" && w != "i" && w != "o" && w != "x"
	}
	return !strings.ContainsAny(w, "aeiou")
}

// isJoinedCode accepts single-token codes: optional 1-2 leading digits (only
// with a single letter, e.g. "1p31"), 1-3 letters, 1-4 digits.
func isJoinedCode(w string) bool {
	i := 0
	for i < len(w) && i < 3 && w[i] >= '0' && w[i] <= '9' {
		i++
	}
	lead := i
	if lead > 2 {
		return false
	}
	j := i
	for j < len(w) && w[j] >= 'a' && w[j] <= 'z' {
		j++
	}
	letters := w[i:j]
	digits := w[j:]
	if len(letters) == 0 || len(letters) > 3 || !isDigits(digits) || len(digits) > 4 {
		return false
	}
	if lead > 0 && len(letters) != 1 {
		return false
	}
	if codeExclusions[letters] || codeExclusions[w] {
		return false
	}
	if len(letters) == 1 {
		return letters != "i" && letters != "o" && letters != "x"
	}
	return !strings.ContainsAny(letters, "aeiou") || joinedCodeAllow[letters]
}

// ── Positional helpers ───────────────────────────────────────────────

// parseAddressee recognizes a dispatch/command addressee at the start of a
// transmission: an optional one- or two-word place name, a dispatch word, and
// optional trailing agency words ("County", "Butler County", "Warren County
// Fire", "Fire Dispatch", "Command"). Returns the index after it.
func parseAddressee(toks []token, i int) (int, bool) {
	for lead := 0; lead <= 2 && i+lead < len(toks); lead++ {
		j := i + lead
		if toks[j].brk {
			return i, false
		}
		if dispatchWords[toks[j].s] {
			k := j + 1
			for k < len(toks) && k <= j+2 && !toks[k].brk && agencyWords[toks[k].s] {
				k++
			}
			return k, true
		}
		if !isPlainWord(toks[j].s) {
			return i, false
		}
	}
	return i, false
}

func addresseeAt(toks []token, i int) bool {
	_, ok := parseAddressee(toks, i)
	return ok
}

// isPlainWord is a word that can be part of an addressee's place name.
// Function words cannot: "This is County, Medic 12 respond" is the dispatcher
// identifying itself and then calling Medic 12, not "<place> County" called
// by Medic 12.
func isPlainWord(w string) bool {
	if !isLetters(w) || prefixWords[w] || phoneticWords[w] || isNumberish(w) || placeStopWords[w] {
		return false
	}
	return true
}

// anotherUnitFollows reports whether a second designator follows (after
// breaks, fillers or "and") — "County, Medic 12, Engine 5 ..." names several
// units and the speaker is ambiguous.
func anotherUnitFollows(toks []token, i int) bool {
	k := skipLead(toks, i)
	if isWordAt(toks, k, "and") {
		k = skipLead(toks, k+1)
	}
	return designatorAt(toks, k)
}

// statusAt matches a self-reported status phrase at toks[i], optionally after
// a short subject ("is", "we're", "we are", "I'm").
func statusAt(toks []token, i int) bool {
	for skip := 0; skip <= 2; skip++ {
		j := i + skip
		for _, phrase := range statusPhrases {
			if phraseAt(toks, j, phrase) {
				return true
			}
		}
		if j >= len(toks) || toks[j].brk || !statusSubjectWords[toks[j].s] {
			return false
		}
	}
	return false
}

func phraseAt(toks []token, i int, phrase []string) bool {
	if i+len(phrase) > len(toks) {
		return false
	}
	for k, w := range phrase {
		if toks[i+k].brk || toks[i+k].s != w {
			return false
		}
	}
	return true
}

// skipLead skips leading breaks and disfluencies ("uh", "um").
func skipLead(toks []token, i int) int {
	for i < len(toks) && (toks[i].brk || fillerWords[toks[i].s]) {
		i++
	}
	return i
}

func skipBreaks(toks []token, i int) int {
	for i < len(toks) && toks[i].brk {
		i++
	}
	return i
}

// atEnd reports whether only breaks (or a closing "over") remain from toks[i].
func atEnd(toks []token, i int) bool {
	for ; i < len(toks); i++ {
		if !toks[i].brk && toks[i].s != "over" {
			return false
		}
	}
	return true
}

func isWordAt(toks []token, i int, w string) bool {
	return i < len(toks) && !toks[i].brk && toks[i].s == w
}

func isBreakAt(toks []token, i int) bool {
	return i < len(toks) && toks[i].brk
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isLetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

func allParts(parts []string, f func(string) bool) bool {
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if !f(p) {
			return false
		}
	}
	return true
}

func titleWord(w string) string {
	if w == "" {
		return w
	}
	if special, ok := displayOverrides[w]; ok {
		return special
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// ── Vocabulary ───────────────────────────────────────────────────────

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// prefixWords precede a unit number in fire/EMS/law designators.
var prefixWords = set(
	"engine", "ladder", "truck", "tower", "quint", "rescue", "squad", "medic",
	"ambulance", "tanker", "tender", "brush", "battalion", "chief", "safety",
	"utility", "hazmat", "marine", "boat", "air", "car", "unit", "deputy",
	"sergeant", "sarge", "lieutenant", "captain", "trooper", "officer",
	"detective", "patrol", "k9", "canine", "supervisor", "marshal", "constable",
	"warden", "ranger", "rehab", "support", "wagon", "attack",
)

// phoneticWords are APCO and NATO letters used in designators ("Adam 12",
// "1 Paul 31", "Lincoln 5").
var phoneticWords = set(
	// APCO
	"adam", "boy", "charles", "david", "edward", "frank", "george", "henry",
	"ida", "john", "king", "lincoln", "mary", "nora", "ocean", "paul", "queen",
	"robert", "sam", "tom", "union", "victor", "william", "xray", "young", "zebra",
	// NATO (distinct from APCO)
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel",
	"india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa",
	"quebec", "romeo", "sierra", "tango", "uniform", "whiskey", "yankee", "zulu",
)

var twoWordPrefixes = set(
	"battalion chief", "district chief", "deputy chief", "assistant chief",
	"division chief", "fire chief", "heavy rescue", "air medic", "mobile command",
)

// dispatchWords name a dispatch center or command post used as the called
// party at the start of a transmission.
var dispatchWords = set(
	"dispatch", "dispatcher", "county", "control", "comm", "comms", "communications",
	"central", "radio", "base", "headquarters", "hq", "command", "ecc", "eoc", "psap",
)

// agencyWords may trail a dispatch word ("County Fire", "Fire Dispatch").
var agencyWords = set(
	"fire", "ems", "police", "sheriff", "pd", "fd", "so", "dispatch", "county",
	"control", "communications", "comm", "comms",
)

// placeStopWords are function words (pronouns, determiners, copulas,
// prepositions, conjunctions) that never belong to a place name before a
// dispatch word. Apostrophes are stripped by the tokenizer ("it's" → "its").
var placeStopWords = set(
	"this", "that", "these", "those", "here", "there", "it", "its", "is", "are",
	"was", "be", "the", "a", "an", "to", "for", "at", "from", "with", "of", "on",
	"in", "into", "by", "and", "or", "but", "go", "i", "im", "we", "were", "you",
	"your", "youre", "he", "she", "they", "my", "our", "his", "her", "their",
	"me", "us", "him", "them",
)

var statusPhrases = [][]string{
	{"on", "scene"}, {"on", "the", "scene"}, {"onscene"}, {"on", "location"},
	{"arrived"}, {"arriving"}, {"responding"}, {"en", "route"}, {"enroute"},
	{"in", "route"}, {"returning"}, {"clear"}, {"cleared"}, {"in", "service"},
	{"back", "in", "service"}, {"available"}, {"in", "quarters"},
	{"out", "of", "service"}, {"staging"}, {"staged"}, {"transporting"},
	{"copies"}, {"out", "at"}, {"out", "with"}, {"on", "the", "way"},
	{"10-8"}, {"10-97"}, {"10-23"}, {"10-76"}, {"10-7"},
}

// statusSubjectWords may sit between the ID and a status phrase (at most two).
// Apostrophes are stripped by the tokenizer: "we're" → "were", "I'm" → "im",
// "we'll" → "well", "I'll" → "ill". "show us/me" is the speaker reporting
// their own status; "show you" (a dispatcher confirming a unit's status) is
// deliberately absent.
var statusSubjectWords = set("is", "were", "we", "are", "im", "i", "am", "will", "be", "well", "ill",
	"show", "us", "me")

var fillerWords = set("uh", "um", "umm", "uhm", "uhh", "er", "ah", "hm", "hmm")

// quantityWords after a number mean it is a quantity, not a unit number.
var quantityWords = set(
	"minute", "minutes", "min", "mins", "second", "seconds", "sec", "secs",
	"hour", "hours", "hr", "hrs", "mile", "miles", "block", "blocks", "feet",
	"foot", "ft", "yard", "yards", "percent", "year", "years", "yr", "yrs",
	"month", "months", "day", "days", "week", "weeks", "units", "cars",
	"vehicle", "vehicles", "patient", "patients", "people", "person", "persons",
	"pound", "pounds", "lb", "lbs", "degree", "degrees", "am", "pm", "oclock",
	"times", "story", "stories", "hundred", "thousand", "mph", "north", "south",
	"east", "west", "northbound", "southbound", "eastbound", "westbound",
)

// codeExclusions are letter prefixes (and whole tokens) that commonly precede
// numbers without being a unit: roads, times, measurements, channels.
var codeExclusions = set(
	"i", "sr", "us", "rt", "rte", "cr", "hwy", "st", "rd", "dr", "ln", "ct",
	"mm", "mi", "ft", "hr", "hrs", "min", "sec", "mph", "kph", "lb", "lbs",
	"kg", "mg", "ml", "cc", "bp", "pt", "pts", "rm", "fl", "ch", "tg", "fg",
	"grp", "ext", "px", "rx", "tx", "sn", "vs", "yr", "yrs", "wk", "am", "pm",
	"th", "nd", "hz", "khz", "mhz", "db", "k9", "mp", "tac", "ops",
	// vowel-less English words that would otherwise look like letter codes
	"by", "my", "why", "try", "fly", "dry", "cry", "sky", "shy", "gym", "spy",
	"pry", "fry", "hmm", "brr", "mr", "ms", "mrs", "jr", "u",
)

// joinedCodeAllow permits a few vowel-bearing abbreviations in joined codes.
var joinedCodeAllow = set("eng", "med", "amb", "res", "bat")

var displayOverrides = map[string]string{"k9": "K9", "hq": "HQ"}

var onesWords = map[string]string{
	"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
	"six": "6", "seven": "7", "eight": "8", "nine": "9", "niner": "9",
}

// spokenZeroWords stand for 0 inside a digit-by-digit number ("one oh five").
var spokenZeroWords = set("oh", "o")

var teenWords = map[string]string{
	"ten": "10", "eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14",
	"fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18", "nineteen": "19",
}

var tensWords = map[string]int{
	"twenty": 2, "thirty": 3, "forty": 4, "fifty": 5, "sixty": 6, "seventy": 7,
	"eighty": 8, "ninety": 9,
}
