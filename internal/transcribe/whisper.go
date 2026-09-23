package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// WhisperClient calls an OpenAI-compatible /v1/audio/transcriptions endpoint.
// Implements the Provider interface.
//
// The request and response shape depend on the configured model (see
// modelFamily). whisper-1 and self-hosted Whisper-compatible servers get
// verbose_json with word timestamps; OpenAI's GPT transcription models get the
// formats and fields OpenAI documents for them.
type WhisperClient struct {
	url     string
	model   string
	family  modelFamily
	apiKey  string
	timeout time.Duration
	client  *http.Client

	log    zerolog.Logger
	warned sync.Map // setting name -> struct{}; makes dropped-option warnings log once
}

// modelFamily selects the request fields and response format for a model.
type modelFamily int

const (
	// familyWhisper: whisper-1 and any self-hosted or third-party
	// Whisper-compatible server (speaches, tools/whisper-server, Groq, ...).
	// This is the default for any model name not recognized below. Requests
	// verbose_json with word timestamps plus the tr-engine extended params.
	familyWhisper modelFamily = iota

	// familyGPT4o: gpt-4o-transcribe, gpt-4o-mini-transcribe and their dated
	// snapshots (e.g. gpt-4o-mini-transcribe-2025-12-15). OpenAI accepts only
	// response_format=json for these: text, no timestamps, no duration.
	familyGPT4o

	// familyGPTTranscribe: gpt-transcribe. JSON text plus detected languages,
	// no timestamps. Takes languages[] instead of language, and keywords[].
	familyGPTTranscribe

	// familyDiarize: gpt-4o-transcribe-diarize. response_format=diarized_json
	// returns speaker-labelled segments with start/end times. Prompts are not
	// supported, and chunking_strategy is required for audio longer than 30s.
	familyDiarize
)

// detectModelFamily maps a configured model name to its family. Matching is
// case-insensitive and prefix-based so dated snapshots (gpt-4o-mini-transcribe-2025-12-15)
// resolve to their family, and a provider qualifier such as "openai/" (LiteLLM,
// OpenRouter style) is ignored. Anything unrecognized is treated as Whisper.
func detectModelFamily(model string) modelFamily {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "gpt-") && strings.Contains(m, "transcribe") && strings.Contains(m, "diarize"):
		return familyDiarize
	case strings.HasPrefix(m, "gpt-4o-transcribe"), strings.HasPrefix(m, "gpt-4o-mini-transcribe"):
		return familyGPT4o
	case strings.HasPrefix(m, "gpt-transcribe"):
		return familyGPTTranscribe
	default:
		return familyWhisper
	}
}

// TranscribeOpts are per-request options for the Whisper API.
// Zero-value fields are omitted from the request, preserving backward
// compatibility with servers that ignore unknown form fields (e.g. speaches).
type TranscribeOpts struct {
	// Standard OpenAI params
	Temperature float64
	Language    string
	Prompt      string // initial_prompt / domain vocabulary
	Hotwords    string // vocabulary boost terms

	// Decoding
	BeamSize int // 0 = server default (typically 5)

	// Anti-hallucination
	RepetitionPenalty             float64 // >1.0 penalizes repetition (0 = omit)
	NoRepeatNgramSize             int     // block n-gram repetition (0 = disabled)
	ConditionOnPreviousText       *bool   // nil = omit (server default); false = prevent cascading
	NoSpeechThreshold             float64 // 0 = omit (server default ~0.6)
	HallucinationSilenceThreshold float64 // 0 = omit/disabled
	MaxNewTokens                  int     // 0 = omit/unlimited

	// VAD
	VadFilter bool
}

// whisperResponse is the parsed response from the Whisper API (verbose_json format).
type whisperResponse struct {
	Text     string        `json:"text"`
	Language string        `json:"language"`
	Duration float64       `json:"duration"`
	Words    []whisperWord `json:"words"`
}

// whisperWord is a word with start/end timestamps from Whisper.
type whisperWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// openAIJSONResponse is the response_format=json body from OpenAI's GPT
// transcription models. languages is only returned by gpt-transcribe.
type openAIJSONResponse struct {
	Text      string `json:"text"`
	Languages []struct {
		Code string `json:"code"`
	} `json:"languages"`
}

// diarizedResponse is the response_format=diarized_json body from
// gpt-4o-transcribe-diarize.
type diarizedResponse struct {
	Text     string            `json:"text"`
	Duration float64           `json:"duration"`
	Segments []diarizedSegment `json:"segments"`
}

// diarizedSegment is one speaker-labelled segment. Speakers are "A", "B", ...
// unless known_speaker_names[] were supplied (tr-engine does not send those).
type diarizedSegment struct {
	Speaker string  `json:"speaker"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
}

// NewWhisperClient creates a new Whisper HTTP client.
func NewWhisperClient(url, model, apiKey string, timeout time.Duration) *WhisperClient {
	return &WhisperClient{
		url:     url,
		model:   model,
		family:  detectModelFamily(model),
		apiKey:  apiKey,
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
		log:     zerolog.Nop(),
	}
}

// SetLogger assigns a real logger (replaces the default no-op logger). Used to
// warn once about configured options the selected model does not accept.
func (wc *WhisperClient) SetLogger(l zerolog.Logger) {
	wc.log = l
}

// Name returns the provider name.
func (wc *WhisperClient) Name() string { return "whisper" }

// Model returns the configured model identifier.
func (wc *WhisperClient) Model() string { return wc.model }

// Transcribe sends an audio file to the Whisper API and returns the result.
// Uses multipart/form-data. Only non-default parameters are sent, so this
// works with speaches, the custom whisper-server, or any OpenAI-compatible endpoint.
func (wc *WhisperClient) Transcribe(ctx context.Context, audioPath string, opts TranscribeOpts) (*Response, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("open audio file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Audio file field
	part, err := w.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, fmt.Errorf("copy audio data: %w", err)
	}

	wc.writeFields(w, opts)

	w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wc.url, &buf)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if wc.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+wc.apiKey)
	}

	resp, err := wc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whisper request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whisper API error (status %d): %s", resp.StatusCode, string(body))
	}

	return parseTranscription(wc.family, body)
}

// writeFields writes every form field after the audio file. For familyWhisper
// the fields, values and order are exactly what tr-engine sent before OpenAI's
// GPT transcription models were supported.
func (wc *WhisperClient) writeFields(w *multipart.Writer, opts TranscribeOpts) {
	// Model
	if wc.model != "" {
		w.WriteField("model", wc.model)
	}

	// Language
	lang := opts.Language
	if lang == "" {
		lang = "en"
	}
	if wc.family == familyGPTTranscribe {
		// gpt-transcribe replaces the singular language field with a
		// languages[] hint list; OpenAI says not to send both.
		w.WriteField("languages[]", lang)
	} else {
		w.WriteField("language", lang)
	}

	// Temperature
	w.WriteField("temperature", fmt.Sprintf("%.2f", opts.Temperature))

	switch wc.family {
	case familyGPT4o:
		// json is the only format these models accept (no verbose_json,
		// no timestamp_granularities), so there are no word timestamps.
		w.WriteField("response_format", "json")
		wc.writeOpenAIFields(w, opts)
	case familyGPTTranscribe:
		// response_format is left at its default (json), as in OpenAI's
		// gpt-transcribe examples. No word timestamps either.
		wc.writeOpenAIFields(w, opts)
	case familyDiarize:
		// diarized_json is required to receive speaker-labelled segments.
		w.WriteField("response_format", "diarized_json")
		// Required for inputs longer than 30s; radio calls can exceed that
		// and "auto" is harmless for short ones, so always send it.
		w.WriteField("chunking_strategy", "auto")
		wc.writeOpenAIFields(w, opts)
	default:
		// Response format: verbose_json for word-level timestamps
		w.WriteField("response_format", "verbose_json")

		// Request word-level timestamps
		w.WriteField("timestamp_granularities[]", "word")

		writeWhisperExtendedFields(w, opts)
	}
}

// writeWhisperExtendedFields writes the tr-engine extended parameters understood
// by Whisper-compatible servers (only sent when non-default).
func writeWhisperExtendedFields(w *multipart.Writer, opts TranscribeOpts) {
	if opts.Prompt != "" {
		w.WriteField("prompt", opts.Prompt)
	}

	if opts.Hotwords != "" {
		w.WriteField("hotwords", opts.Hotwords)
	}

	if opts.BeamSize > 0 {
		w.WriteField("beam_size", fmt.Sprintf("%d", opts.BeamSize))
	}

	if opts.RepetitionPenalty > 0 && opts.RepetitionPenalty != 1.0 {
		w.WriteField("repetition_penalty", fmt.Sprintf("%.2f", opts.RepetitionPenalty))
	}

	if opts.NoRepeatNgramSize > 0 {
		w.WriteField("no_repeat_ngram_size", fmt.Sprintf("%d", opts.NoRepeatNgramSize))
	}

	if opts.ConditionOnPreviousText != nil {
		if *opts.ConditionOnPreviousText {
			w.WriteField("condition_on_previous_text", "true")
		} else {
			w.WriteField("condition_on_previous_text", "false")
		}
	}

	if opts.NoSpeechThreshold > 0 {
		w.WriteField("no_speech_threshold", fmt.Sprintf("%.2f", opts.NoSpeechThreshold))
	}

	if opts.HallucinationSilenceThreshold > 0 {
		w.WriteField("hallucination_silence_threshold", fmt.Sprintf("%.2f", opts.HallucinationSilenceThreshold))
	}

	if opts.MaxNewTokens > 0 {
		w.WriteField("max_new_tokens", fmt.Sprintf("%d", opts.MaxNewTokens))
	}

	if opts.VadFilter {
		w.WriteField("vad_filter", "true")
	}
}

// writeOpenAIFields writes the optional fields for OpenAI's GPT transcription
// models. OpenAI's published request schema for /v1/audio/transcriptions is
// closed (additionalProperties: false) and has no equivalent of the Whisper
// server extensions (beam_size, anti-hallucination, VAD), so those are never
// sent. Each configured option that gets dropped is logged once.
func (wc *WhisperClient) writeOpenAIFields(w *multipart.Writer, opts TranscribeOpts) {
	if opts.Prompt != "" {
		if wc.family == familyDiarize {
			wc.warnDropped("WHISPER_PROMPT", "gpt-4o-transcribe-diarize does not support prompts")
		} else {
			w.WriteField("prompt", opts.Prompt)
		}
	}

	if opts.Hotwords != "" {
		if wc.family == familyGPTTranscribe {
			// gpt-transcribe takes literal terms as keywords[].
			for _, kw := range wc.keywordsFromHotwords(opts.Hotwords) {
				w.WriteField("keywords[]", kw)
			}
		} else {
			wc.warnDropped("WHISPER_HOTWORDS", "hotwords are only mapped to keywords[] for gpt-transcribe")
		}
	}

	if opts.BeamSize > 0 {
		wc.warnDropped("WHISPER_BEAM_SIZE", "")
	}
	if opts.RepetitionPenalty > 0 && opts.RepetitionPenalty != 1.0 {
		wc.warnDropped("WHISPER_REPETITION_PENALTY", "")
	}
	if opts.NoRepeatNgramSize > 0 {
		wc.warnDropped("WHISPER_NO_REPEAT_NGRAM", "")
	}
	if opts.ConditionOnPreviousText != nil {
		wc.warnDropped("WHISPER_CONDITION_ON_PREV", "")
	}
	if opts.NoSpeechThreshold > 0 {
		wc.warnDropped("WHISPER_NO_SPEECH_THRESHOLD", "")
	}
	if opts.HallucinationSilenceThreshold > 0 {
		wc.warnDropped("WHISPER_HALLUCINATION_THRESHOLD", "")
	}
	if opts.MaxNewTokens > 0 {
		wc.warnDropped("WHISPER_MAX_TOKENS", "")
	}
	if opts.VadFilter {
		wc.warnDropped("WHISPER_VAD_FILTER", "")
	}
}

// keywordsFromHotwords splits comma-separated hotwords into gpt-transcribe
// keywords. OpenAI rejects the whole request if a keyword contains '<', '>',
// CR or LF, so such terms are skipped (and logged once each).
func (wc *WhisperClient) keywordsFromHotwords(hotwords string) []string {
	var out []string
	for _, kw := range strings.Split(hotwords, ",") {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if strings.ContainsAny(kw, "<>\r\n") {
			wc.warnDropped("WHISPER_HOTWORDS term "+kw, "OpenAI rejects keywords containing '<', '>', or line breaks")
			continue
		}
		out = append(out, kw)
	}
	return out
}

// warnDropped logs, once per setting for the life of the client, that a
// configured option is not being sent to the selected model.
func (wc *WhisperClient) warnDropped(setting, reason string) {
	if _, seen := wc.warned.LoadOrStore(setting, struct{}{}); seen {
		return
	}
	ev := wc.log.Warn().Str("model", wc.model).Str("setting", setting)
	if reason != "" {
		ev = ev.Str("reason", reason)
	}
	ev.Msg("setting not supported by this transcription model; not sending it (logged once)")
}

// parseTranscription decodes a successful response body in the format that was
// requested for family.
func parseTranscription(family modelFamily, body []byte) (*Response, error) {
	switch family {
	case familyGPT4o, familyGPTTranscribe:
		var result openAIJSONResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		// No word timestamps and no duration in this format. Words stay nil,
		// so unit attribution is skipped and the worker falls back to the
		// call's own duration and a text-based word count.
		resp := &Response{Text: result.Text}
		if len(result.Languages) > 0 {
			resp.Language = result.Languages[0].Code
		}
		return resp, nil

	case familyDiarize:
		var result diarizedResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return diarizedToResponse(result), nil

	default:
		var result whisperResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		// Convert internal types to common Response/Word types
		words := make([]Word, len(result.Words))
		for i, ww := range result.Words {
			words[i] = Word{Word: ww.Word, Start: ww.Start, End: ww.End}
		}

		return &Response{
			Text:     result.Text,
			Language: result.Language,
			Duration: result.Duration,
			Words:    words,
		}, nil
	}
}

// diarizedToResponse converts speaker-labelled segments into the common
// Response.
//
// The diarize model only gives segment-level start/end, so per-word timings are
// APPROXIMATED by spreading each segment's words evenly across its span. That
// is good enough for src_list unit attribution, which works at transmission
// granularity, but these are not real word timestamps. Each word carries its
// segment's speaker label and utterance number, which AttributeWords uses to
// keep a segment that starts or ends in src_list's lag window from being split
// across two units.
//
// Text is rebuilt by joining the segment texts rather than using the top-level
// text field: OpenAI's examples show that field with "Speaker: " prefixes on
// each line, which we don't want in stored text or full-text search. Joining
// the segments also keeps Text consistent with the synthesized words. The
// top-level text is used only when there are no usable segments.
func diarizedToResponse(result diarizedResponse) *Response {
	var words []Word
	var texts []string
	for _, seg := range result.Segments {
		segText := strings.TrimSpace(seg.Text)
		if segText == "" {
			continue
		}
		texts = append(texts, segText)
		for _, w := range interpolateWords(segText, seg.Start, seg.End) {
			w.Speaker = seg.Speaker
			w.Utterance = len(texts)
			words = append(words, w)
		}
	}

	text := result.Text
	if len(texts) > 0 {
		text = strings.Join(texts, " ")
	}

	return &Response{
		Text:     text,
		Duration: result.Duration,
		Words:    words,
	}
}
