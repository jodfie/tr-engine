package transcribe

import (
	"context"
	"math"
	"strings"
)

// Provider is the interface for speech-to-text backends.
type Provider interface {
	Transcribe(ctx context.Context, audioPath string, opts TranscribeOpts) (*Response, error)
	Name() string  // "whisper", "elevenlabs", "deepinfra"
	Model() string // model identifier for DB/logs
}

// Response is the common transcription result from any provider.
type Response struct {
	Text     string
	Language string
	Duration float64 // audio duration in seconds (0 if the provider doesn't report it)
	Words    []Word  // nil if provider doesn't support word timestamps
}

// Word is a timestamped word from any STT provider.
type Word struct {
	Word    string
	Start   float64 // seconds
	End     float64 // seconds
	Speaker string  // diarization speaker label (e.g. "A"); empty unless the provider diarizes
	// Utterance numbers (from 1) the single-speaker segment this word's
	// APPROXIMATE timing was interpolated from, so attribution can treat the
	// segment as a whole near transmission boundaries. 0 = real word timestamp
	// (or segment timing from a non-diarizing provider).
	Utterance int
}

// interpolateWords splits a segment's text into words and spreads them evenly
// across [start, end), rounded to the millisecond. The resulting timings are
// APPROXIMATE — they are only used when a provider returns segment-level
// timestamps without word-level ones, so that unit attribution (which works at
// transmission granularity) still has something to go on.
func interpolateWords(text string, start, end float64) []Word {
	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return nil
	}
	dur := end - start
	if dur < 0 {
		dur = 0
	}
	wordDur := dur / float64(len(tokens))
	words := make([]Word, len(tokens))
	for i, tok := range tokens {
		words[i] = Word{
			Word:  tok,
			Start: roundMs(start + float64(i)*wordDur),
			End:   roundMs(start + float64(i+1)*wordDur),
		}
	}
	return words
}

func roundMs(sec float64) float64 {
	return math.Round(sec*1000) / 1000
}
