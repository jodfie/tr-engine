package ingest

import (
	"encoding/json"
	"testing"
)

func TestCallDataUnitAlphaTagOTA(t *testing.T) {
	t.Run("call_start_with_ota", func(t *testing.T) {
		payload := []byte(`{
			"type": "call_start",
			"timestamp": 1700000000,
			"instance_id": "tr-1",
			"call": {
				"id": "1_338_1700000000",
				"sys_name": "nswgrn",
				"unit": 338,
				"unit_alpha_tag": "FRNSW - P 338 - Jindabyne",
				"unit_alpha_tag_ota": "P338 FF1",
				"talkgroup": 200
			}
		}`)
		var msg CallStartMsg
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Call.UnitAlphaTag != "FRNSW - P 338 - Jindabyne" {
			t.Errorf("UnitAlphaTag = %q", msg.Call.UnitAlphaTag)
		}
		if msg.Call.UnitAlphaTagOTA != "P338 FF1" {
			t.Errorf("UnitAlphaTagOTA = %q, want %q", msg.Call.UnitAlphaTagOTA, "P338 FF1")
		}
	})

	t.Run("call_end_without_ota", func(t *testing.T) {
		payload := []byte(`{
			"type": "call_end",
			"timestamp": 1700000010,
			"instance_id": "tr-1",
			"call": {"id": "1_338_1700000000", "sys_name": "nswgrn", "unit": 338, "unit_alpha_tag": "P338 FF1"}
		}`)
		var msg CallEndMsg
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Call.UnitAlphaTagOTA != "" {
			t.Errorf("UnitAlphaTagOTA = %q, want empty", msg.Call.UnitAlphaTagOTA)
		}
	})
}

// trunk-recorder 5.2+ writes each source's raw OTA alias as srcList[].tag_ota in
// the call JSON (the .json sidecar read by file-watch, and MQTT audio metadata).
func TestAudioMetadataSrcListTagOTA(t *testing.T) {
	payload := []byte(`{
		"talkgroup": 200,
		"short_name": "nswgrn",
		"srcList": [
			{"src": 338, "time": 1700000000, "pos": 0.00, "emergency": 0, "signal_system": "", "tag": "FRNSW - P 338 - Jindabyne", "tag_ota": "P338 FF1"},
			{"src": 339, "time": 1700000003, "pos": 3.00, "emergency": 0, "signal_system": "", "tag": "", "tag_ota": ""},
			{"src": 340, "time": 1700000005, "pos": 5.00, "emergency": 0, "signal_system": "", "tag": "E340"}
		]
	}`)
	var meta AudioMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(meta.SrcList) != 3 {
		t.Fatalf("SrcList length = %d, want 3", len(meta.SrcList))
	}
	for i, want := range []struct{ tag, tagOTA string }{
		{"FRNSW - P 338 - Jindabyne", "P338 FF1"},
		{"", ""},     // explicit empty alias
		{"E340", ""}, // pre-5.2 trunk-recorder: no tag_ota key
	} {
		if got := meta.SrcList[i]; got.Tag != want.tag || got.TagOTA != want.tagOTA {
			t.Errorf("SrcList[%d] tag/tag_ota = %q/%q, want %q/%q", i, got.Tag, got.TagOTA, want.tag, want.tagOTA)
		}
	}
}
