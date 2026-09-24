package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// formField is one multipart part as received by the server, in order.
type formField struct {
	Name  string
	Value string // file contents for the "file" part
}

// captureServer is an httptest server that records the ordered multipart parts
// of each request and replies with a canned body.
type captureServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests [][]formField
	auth     []string
}

func newCaptureServer(t *testing.T, status int, body string) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("bad content type: %v", err)
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var fields []formField
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("read part: %v", err)
				return
			}
			b, _ := io.ReadAll(p)
			fields = append(fields, formField{Name: p.FormName(), Value: string(b)})
		}
		cs.mu.Lock()
		cs.requests = append(cs.requests, fields)
		cs.auth = append(cs.auth, r.Header.Get("Authorization"))
		cs.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *captureServer) lastRequest(t *testing.T) []formField {
	t.Helper()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.requests) == 0 {
		t.Fatal("server received no requests")
	}
	return cs.requests[len(cs.requests)-1]
}

func writeTestAudio(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(p, []byte("fake-audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// allOpts sets every option to a non-default value so the tests show exactly
// which ones each model family sends.
func allOpts() TranscribeOpts {
	f := false
	return TranscribeOpts{
		Temperature:                   0.1,
		Language:                      "en",
		Prompt:                        "Medic 23, Engine 7.",
		Hotwords:                      "Medic,Engine",
		BeamSize:                      5,
		RepetitionPenalty:             1.2,
		NoRepeatNgramSize:             3,
		ConditionOnPreviousText:       &f,
		NoSpeechThreshold:             0.6,
		HallucinationSilenceThreshold: 2.0,
		MaxNewTokens:                  128,
		VadFilter:                     true,
	}
}

const verboseJSONBody = `{"task":"transcribe","language":"english","duration":2.5,"text":"Engine 7 responding.","words":[{"word":"Engine","start":0.0,"end":0.4},{"word":"7","start":0.4,"end":0.7},{"word":"responding","start":0.8,"end":1.5}]}`

func TestDetectModelFamily(t *testing.T) {
	tests := []struct {
		model string
		want  modelFamily
	}{
		{"", familyWhisper},
		{"whisper-1", familyWhisper},
		{"WHISPER-1", familyWhisper},
		{"deepdml/faster-whisper-large-v3-turbo-ct2", familyWhisper},
		{"Systran/faster-whisper-large-v3", familyWhisper},
		{"large-v3", familyWhisper},
		{"whisper-large-v3-turbo", familyWhisper},
		{"gpt-4o", familyWhisper}, // chat model, not a transcription model
		{"gpt-4o-transcribe", familyGPT4o},
		{"GPT-4o-Transcribe", familyGPT4o},
		{"gpt-4o-mini-transcribe", familyGPT4o},
		{"gpt-4o-mini-transcribe-2025-03-20", familyGPT4o},
		{"gpt-4o-mini-transcribe-2025-12-15", familyGPT4o},
		{"openai/gpt-4o-transcribe", familyGPT4o},
		{" gpt-4o-transcribe ", familyGPT4o},
		{"gpt-4o-transcribe-diarize", familyDiarize},
		{"openai/GPT-4o-Transcribe-Diarize", familyDiarize},
		{"gpt-transcribe", familyGPTTranscribe},
		{"openai/gpt-transcribe", familyGPTTranscribe},
	}
	for _, tt := range tests {
		if got := detectModelFamily(tt.model); got != tt.want {
			t.Errorf("detectModelFamily(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestWhisperClient_RequestFields(t *testing.T) {
	tests := []struct {
		name  string
		model string
		opts  TranscribeOpts
		body  string
		want  []formField
	}{
		{
			// whisper-1 must send exactly what tr-engine always sent.
			name:  "whisper-1 all options",
			model: "whisper-1",
			opts:  allOpts(),
			body:  verboseJSONBody,
			want: []formField{
				{"file", "fake-audio"},
				{"model", "whisper-1"},
				{"language", "en"},
				{"temperature", "0.10"},
				{"response_format", "verbose_json"},
				{"timestamp_granularities[]", "word"},
				{"prompt", "Medic 23, Engine 7."},
				{"hotwords", "Medic,Engine"},
				{"beam_size", "5"},
				{"repetition_penalty", "1.20"},
				{"no_repeat_ngram_size", "3"},
				{"condition_on_previous_text", "false"},
				{"no_speech_threshold", "0.60"},
				{"hallucination_silence_threshold", "2.00"},
				{"max_new_tokens", "128"},
				{"vad_filter", "true"},
			},
		},
		{
			name:  "custom whisper server model, default options",
			model: "deepdml/faster-whisper-large-v3-turbo-ct2",
			opts:  TranscribeOpts{},
			body:  verboseJSONBody,
			want: []formField{
				{"file", "fake-audio"},
				{"model", "deepdml/faster-whisper-large-v3-turbo-ct2"},
				{"language", "en"},
				{"temperature", "0.00"},
				{"response_format", "verbose_json"},
				{"timestamp_granularities[]", "word"},
			},
		},
		{
			name:  "no model configured",
			model: "",
			opts:  TranscribeOpts{Temperature: 0.1, Language: "de"},
			body:  verboseJSONBody,
			want: []formField{
				{"file", "fake-audio"},
				{"language", "de"},
				{"temperature", "0.10"},
				{"response_format", "verbose_json"},
				{"timestamp_granularities[]", "word"},
			},
		},
		{
			name:  "gpt-4o-transcribe",
			model: "gpt-4o-transcribe",
			opts:  allOpts(),
			body:  `{"text":"Engine 7 responding."}`,
			want: []formField{
				{"file", "fake-audio"},
				{"model", "gpt-4o-transcribe"},
				{"language", "en"},
				{"temperature", "0.10"},
				{"response_format", "json"},
				{"prompt", "Medic 23, Engine 7."},
			},
		},
		{
			name:  "dated gpt-4o-mini-transcribe snapshot",
			model: "gpt-4o-mini-transcribe-2025-12-15",
			opts:  allOpts(),
			body:  `{"text":"Engine 7 responding."}`,
			want: []formField{
				{"file", "fake-audio"},
				{"model", "gpt-4o-mini-transcribe-2025-12-15"},
				{"language", "en"},
				{"temperature", "0.10"},
				{"response_format", "json"},
				{"prompt", "Medic 23, Engine 7."},
			},
		},
		{
			name:  "gpt-4o-transcribe-diarize",
			model: "gpt-4o-transcribe-diarize",
			opts:  allOpts(),
			body:  `{"task":"transcribe","duration":1,"text":"x","segments":[]}`,
			want: []formField{
				{"file", "fake-audio"},
				{"model", "gpt-4o-transcribe-diarize"},
				{"language", "en"},
				{"temperature", "0.10"},
				{"response_format", "diarized_json"},
				{"chunking_strategy", "auto"},
			},
		},
		{
			name:  "gpt-transcribe maps language and hotwords",
			model: "gpt-transcribe",
			opts: func() TranscribeOpts {
				o := allOpts()
				o.Hotwords = " Medic, Engine ,,<b>bad</b>,10-4"
				return o
			}(),
			body: `{"text":"Engine 7 responding.","languages":[{"code":"en"}]}`,
			want: []formField{
				{"file", "fake-audio"},
				{"model", "gpt-transcribe"},
				{"languages[]", "en"},
				{"temperature", "0.10"},
				{"prompt", "Medic 23, Engine 7."},
				{"keywords[]", "Medic"},
				{"keywords[]", "Engine"},
				{"keywords[]", "10-4"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCaptureServer(t, http.StatusOK, tt.body)
			wc := NewWhisperClient(srv.URL, tt.model, "sk-test", 5*time.Second)
			if _, err := wc.Transcribe(context.Background(), writeTestAudio(t), tt.opts); err != nil {
				t.Fatalf("Transcribe: %v", err)
			}
			got := srv.lastRequest(t)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("form fields mismatch\n got: %v\nwant: %v", got, tt.want)
			}
			if srv.auth[0] != "Bearer sk-test" {
				t.Errorf("Authorization = %q, want %q", srv.auth[0], "Bearer sk-test")
			}
		})
	}
}

func TestWhisperClient_DroppedOptionsLoggedOnce(t *testing.T) {
	srv := newCaptureServer(t, http.StatusOK, `{"text":"ok"}`)
	var logBuf bytes.Buffer
	wc := NewWhisperClient(srv.URL, "gpt-4o-transcribe", "", 5*time.Second)
	wc.SetLogger(zerolog.New(&logBuf))

	audio := writeTestAudio(t)
	for i := 0; i < 3; i++ {
		if _, err := wc.Transcribe(context.Background(), audio, allOpts()); err != nil {
			t.Fatalf("Transcribe #%d: %v", i, err)
		}
	}

	logs := logBuf.String()
	for _, setting := range []string{
		"WHISPER_HOTWORDS", "WHISPER_BEAM_SIZE", "WHISPER_REPETITION_PENALTY",
		"WHISPER_NO_REPEAT_NGRAM", "WHISPER_CONDITION_ON_PREV", "WHISPER_NO_SPEECH_THRESHOLD",
		"WHISPER_HALLUCINATION_THRESHOLD", "WHISPER_MAX_TOKENS", "WHISPER_VAD_FILTER",
	} {
		if n := strings.Count(logs, `"setting":"`+setting+`"`); n != 1 {
			t.Errorf("%s logged %d times across 3 requests, want 1", setting, n)
		}
	}
	if strings.Contains(logs, "WHISPER_PROMPT") {
		t.Error("prompt is supported by gpt-4o-transcribe and should not be reported as dropped")
	}
}

func TestWhisperClient_DiarizeDropsPromptWithWarning(t *testing.T) {
	srv := newCaptureServer(t, http.StatusOK, `{"task":"transcribe","duration":1,"text":"","segments":[]}`)
	var logBuf bytes.Buffer
	wc := NewWhisperClient(srv.URL, "gpt-4o-transcribe-diarize", "", 5*time.Second)
	wc.SetLogger(zerolog.New(&logBuf))

	if _, err := wc.Transcribe(context.Background(), writeTestAudio(t), TranscribeOpts{Prompt: "Medic 23"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range srv.lastRequest(t) {
		if f.Name == "prompt" {
			t.Error("diarize request must not include prompt")
		}
	}
	if !strings.Contains(logBuf.String(), `"setting":"WHISPER_PROMPT"`) {
		t.Errorf("expected a WHISPER_PROMPT warning, got logs: %s", logBuf.String())
	}
}

func TestWhisperClient_WhisperFamilyLogsNothing(t *testing.T) {
	srv := newCaptureServer(t, http.StatusOK, verboseJSONBody)
	var logBuf bytes.Buffer
	wc := NewWhisperClient(srv.URL, "whisper-1", "", 5*time.Second)
	wc.SetLogger(zerolog.New(&logBuf))
	if _, err := wc.Transcribe(context.Background(), writeTestAudio(t), allOpts()); err != nil {
		t.Fatal(err)
	}
	if logBuf.Len() != 0 {
		t.Errorf("whisper family should not log dropped options, got: %s", logBuf.String())
	}
}

func TestWhisperClient_APIError(t *testing.T) {
	srv := newCaptureServer(t, http.StatusBadRequest, `{"error":{"message":"chunking_strategy is required for diarization models"}}`)
	wc := NewWhisperClient(srv.URL, "whisper-1", "", 5*time.Second)
	_, err := wc.Transcribe(context.Background(), writeTestAudio(t), TranscribeOpts{})
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("expected status 400 error, got %v", err)
	}
}

func TestParseTranscription_VerboseJSON(t *testing.T) {
	resp, err := parseTranscription(familyWhisper, []byte(verboseJSONBody))
	if err != nil {
		t.Fatal(err)
	}
	want := &Response{
		Text:     "Engine 7 responding.",
		Language: "english",
		Duration: 2.5,
		Words: []Word{
			{Word: "Engine", Start: 0.0, End: 0.4},
			{Word: "7", Start: 0.4, End: 0.7},
			{Word: "responding", Start: 0.8, End: 1.5},
		},
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("got %+v\nwant %+v", resp, want)
	}
}

func TestParseTranscription_VerboseJSONNoWords(t *testing.T) {
	// A Whisper-compatible server that ignores timestamp_granularities.
	resp, err := parseTranscription(familyWhisper, []byte(`{"text":"hello","language":"en","duration":1.2}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" || resp.Duration != 1.2 || len(resp.Words) != 0 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestParseTranscription_JSON(t *testing.T) {
	resp, err := parseTranscription(familyGPT4o, []byte(`{"text":"Engine 7 responding.","usage":{"type":"tokens","input_tokens":14,"output_tokens":5,"total_tokens":19}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Engine 7 responding." {
		t.Errorf("Text = %q", resp.Text)
	}
	if resp.Words != nil {
		t.Errorf("Words = %v, want nil (json has no timestamps)", resp.Words)
	}
	if resp.Duration != 0 || resp.Language != "" {
		t.Errorf("Duration/Language = %v/%q, want 0/empty", resp.Duration, resp.Language)
	}
}

func TestParseTranscription_JSONLanguages(t *testing.T) {
	resp, err := parseTranscription(familyGPTTranscribe, []byte(`{"text":"Bonjour","languages":[{"code":"fr"},{"code":"en"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Language != "fr" {
		t.Errorf("Language = %q, want fr", resp.Language)
	}
	resp, err = parseTranscription(familyGPTTranscribe, []byte(`{"text":"...","languages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Language != "" {
		t.Errorf("Language = %q, want empty when no language detected", resp.Language)
	}
}

func TestParseTranscription_DiarizedJSON(t *testing.T) {
	// Top-level text deliberately carries "Speaker: " prefixes, as in OpenAI's
	// documented example; stored text should come from the segments instead.
	body := `{
		"task": "transcribe",
		"duration": 9.5,
		"text": "A: Engine 7 responding.\nB: Copy Engine 7.",
		"segments": [
			{"type":"transcript.text.segment","id":"seg_001","start":0.0,"end":3.0,"text":"Engine 7 responding.","speaker":"A"},
			{"type":"transcript.text.segment","id":"seg_002","start":3.0,"end":3.0,"text":"   ","speaker":"A"},
			{"type":"transcript.text.segment","id":"seg_003","start":5.0,"end":6.5,"text":" Copy Engine 7. ","speaker":"B"}
		],
		"usage": {"type":"duration","seconds":10}
	}`
	resp, err := parseTranscription(familyDiarize, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Engine 7 responding. Copy Engine 7." {
		t.Errorf("Text = %q", resp.Text)
	}
	if resp.Duration != 9.5 {
		t.Errorf("Duration = %v, want 9.5", resp.Duration)
	}
	// Utterances number the non-blank segments, so the blank seg_002 is skipped.
	want := []Word{
		{Word: "Engine", Start: 0, End: 1, Speaker: "A", Utterance: 1},
		{Word: "7", Start: 1, End: 2, Speaker: "A", Utterance: 1},
		{Word: "responding.", Start: 2, End: 3, Speaker: "A", Utterance: 1},
		{Word: "Copy", Start: 5, End: 5.5, Speaker: "B", Utterance: 2},
		{Word: "Engine", Start: 5.5, End: 6, Speaker: "B", Utterance: 2},
		{Word: "7.", Start: 6, End: 6.5, Speaker: "B", Utterance: 2},
	}
	if len(resp.Words) != len(want) {
		t.Fatalf("got %d words, want %d: %+v", len(resp.Words), len(want), resp.Words)
	}
	for i := range want {
		g := resp.Words[i]
		if g.Word != want[i].Word || g.Speaker != want[i].Speaker || g.Utterance != want[i].Utterance ||
			math.Abs(g.Start-want[i].Start) > 1e-9 || math.Abs(g.End-want[i].End) > 1e-9 {
			t.Errorf("word %d = %+v, want %+v", i, g, want[i])
		}
	}
}

func TestParseTranscription_DiarizedJSONEdgeCases(t *testing.T) {
	// No segments: fall back to top-level text, no words.
	resp, err := parseTranscription(familyDiarize, []byte(`{"task":"transcribe","duration":0,"text":"hello","segments":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" || resp.Words != nil {
		t.Errorf("no segments: got %+v", resp)
	}

	// Inverted/zero-length segment: words collapse to the start time, no NaN/Inf.
	resp, err = parseTranscription(familyDiarize, []byte(`{"duration":0,"text":"","segments":[{"start":2,"end":1,"text":"ten four","speaker":"A"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range resp.Words {
		if w.Start != 2 || w.End != 2 || math.IsNaN(w.Start) || math.IsInf(w.End, 0) {
			t.Errorf("inverted segment word = %+v, want start=end=2", w)
		}
	}

	// Malformed body is an error, not a panic.
	if _, err := parseTranscription(familyDiarize, []byte(`not json`)); err == nil {
		t.Error("expected decode error")
	}
}

// Diarized words must flow through unit attribution: src comes from src_list,
// speaker is kept on words, and on segments whose words all share it.
func TestDiarizedResponse_UnitAttribution(t *testing.T) {
	body := `{"duration":7,"text":"","segments":[
		{"start":0.1,"end":2.9,"text":"Engine 7 responding.","speaker":"A"},
		{"start":3.2,"end":5.0,"text":"Copy Engine 7.","speaker":"B"}]}`
	resp, err := parseTranscription(familyDiarize, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	srcList := json.RawMessage(`[{"src":1001,"tag":"Engine 7","pos":0.0},{"src":2002,"tag":"Dispatch","pos":3.0}]`)
	txs := ParseSrcList(srcList, resp.Duration)
	tw := AttributeWords(resp.Words, txs, resp.Text)

	if len(tw.Segments) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(tw.Segments), tw.Segments)
	}
	s0, s1 := tw.Segments[0], tw.Segments[1]
	if s0.Src != 1001 || s0.SrcTag != "Engine 7" || s0.Speaker != "A" || s0.Text != "Engine 7 responding." {
		t.Errorf("segment 0 = %+v", s0)
	}
	if s1.Src != 2002 || s1.SrcTag != "Dispatch" || s1.Speaker != "B" || s1.Text != "Copy Engine 7." {
		t.Errorf("segment 1 = %+v", s1)
	}
	for _, w := range tw.Words {
		if w.Speaker == "" {
			t.Errorf("word %q lost its speaker label", w.Word)
		}
	}
}

// Two diarized speakers inside one unit's transmission (or on a channel with
// no src_list) must not produce extra segments: irc-radio-live.html gives each
// transmission at most one segment and would drop the rest. Speaker labels
// stay on the words.
func TestDiarizedResponse_OneSegmentPerUnit(t *testing.T) {
	body := `{"duration":8,"text":"","segments":[
		{"start":0,"end":5,"text":"Engine 7 respond to Main.","speaker":"A"},
		{"start":5,"end":8,"text":"Copy en route now.","speaker":"B"}]}`
	resp, err := parseTranscription(familyDiarize, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, srcList := range []string{`[{"src":1001,"pos":0}]`, `null`} {
		tw := AttributeWords(resp.Words, ParseSrcList(json.RawMessage(srcList), resp.Duration), resp.Text)
		if len(tw.Segments) != 1 {
			t.Fatalf("src_list %s: got %d segments, want 1: %+v", srcList, len(tw.Segments), tw.Segments)
		}
		s := tw.Segments[0]
		if s.Text != "Engine 7 respond to Main. Copy en route now." || s.Speaker != "" || s.Start != 0 || s.End != 8 {
			t.Errorf("src_list %s: segment = %+v", srcList, s)
		}
		speakers := map[string]int{}
		for _, w := range tw.Words {
			speakers[w.Speaker]++
		}
		if speakers["A"] != 5 || speakers["B"] != 4 || len(speakers) != 2 {
			t.Errorf("src_list %s: word speakers = %v, want A:5 B:4", srcList, speakers)
		}
	}
}

// JSON-only models return no words or duration. Attribution must degrade to
// empty arrays (what the IMBE provider already produces), not nil/null or a panic.
func TestJSONResponse_NoWordsAttribution(t *testing.T) {
	resp, err := parseTranscription(familyGPT4o, []byte(`{"text":"Engine 7 responding."}`))
	if err != nil {
		t.Fatal(err)
	}
	txs := ParseSrcList(json.RawMessage(`[{"src":1001,"pos":0.0},{"src":2002,"pos":3.0}]`), resp.Duration)
	tw := AttributeWords(resp.Words, txs, resp.Text)
	b, err := json.Marshal(tw)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"words":[],"segments":[]}` {
		t.Errorf("words JSON = %s", b)
	}
}
