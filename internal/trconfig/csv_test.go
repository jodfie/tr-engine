package trconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestParseUnitCSV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		want       []UnitEntry
		skipped    int
		duplicates int
	}{
		{
			name:  "headerless",
			input: "1001,Engine 1\n1002,Medic 2\n",
			want:  []UnitEntry{{1001, "Engine 1"}, {1002, "Medic 2"}},
		},
		{
			name:  "header row is not counted as skipped",
			input: "Unit ID,Alpha Tag\n1001,Engine 1\n",
			want:  []UnitEntry{{1001, "Engine 1"}},
		},
		{
			name:  "UTF-8 BOM before header",
			input: "\ufeffUnit ID,Alpha Tag\n1001,Engine 1\n",
			want:  []UnitEntry{{1001, "Engine 1"}},
		},
		{
			name:  "UTF-8 BOM before first data row keeps that row",
			input: "\ufeff1001,Engine 1\n1002,Medic 2\n",
			want:  []UnitEntry{{1001, "Engine 1"}, {1002, "Medic 2"}},
		},
		{
			name:  "quoted tags with commas and escaped quotes",
			input: "1001,\"Engine 1, Station 4\"\n1002,\"Chief \"\"Bob\"\"\"\n",
			want:  []UnitEntry{{1001, "Engine 1, Station 4"}, {1002, `Chief "Bob"`}},
		},
		{
			name:  "CRLF line endings and surrounding whitespace",
			input: " 1001 ,  Engine 1 \r\n1002,Medic 2\r\n",
			want:  []UnitEntry{{1001, "Engine 1"}, {1002, "Medic 2"}},
		},
		{
			name:  "blank and whitespace-only lines are ignored",
			input: "\n1001,Engine 1\n\n   \n,\n1002,Medic 2\n\n",
			want:  []UnitEntry{{1001, "Engine 1"}, {1002, "Medic 2"}},
		},
		{
			name:  "extra columns are ignored",
			input: "1001,Engine 1,Fire,extra\n",
			want:  []UnitEntry{{1001, "Engine 1"}},
		},
		{
			name:    "bad rows after the header are skipped",
			input:   "Unit ID,Alpha Tag\nabc,Not A Unit\n1001,Engine 1\n0,Zero\n-5,Negative\n1003\n1004,\n1005,  \n/^12\\d+$/,Regex\n1002,Medic 2\n",
			want:    []UnitEntry{{1001, "Engine 1"}, {1002, "Medic 2"}},
			skipped: 7,
		},
		{
			name:    "unit IDs beyond 32 bits are skipped",
			input:   "2147483647,Max\n2147483648,Too Big\n99999999999,Way Too Big\n",
			want:    []UnitEntry{{2147483647, "Max"}},
			skipped: 2,
		},
		{
			name:    "non-numeric first row after data is skipped, not a header",
			input:   "1001,Engine 1\nUnit ID,Alpha Tag\n",
			want:    []UnitEntry{{1001, "Engine 1"}},
			skipped: 1,
		},
		{
			name:       "duplicates are kept in order and counted",
			input:      "1001,Old Name\n1002,Medic 2\n1001,New Name\n",
			want:       []UnitEntry{{1001, "Old Name"}, {1002, "Medic 2"}, {1001, "New Name"}},
			duplicates: 1,
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "header only",
			input: "Unit ID,Alpha Tag\n",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseUnitCSV(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("ParseUnitCSV() error = %v", err)
			}
			if !reflect.DeepEqual(got.Entries, tt.want) {
				t.Errorf("Entries = %#v, want %#v", got.Entries, tt.want)
			}
			if got.Skipped != tt.skipped {
				t.Errorf("Skipped = %d, want %d", got.Skipped, tt.skipped)
			}
			if got.Duplicates != tt.duplicates {
				t.Errorf("Duplicates = %d, want %d", got.Duplicates, tt.duplicates)
			}
		})
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestParseUnitCSV_ReadError(t *testing.T) {
	t.Parallel()
	readErr := errors.New("connection reset")
	if _, err := ParseUnitCSV(failingReader{readErr}); !errors.Is(err, readErr) {
		t.Fatalf("ParseUnitCSV() error = %v, want wrapped %v", err, readErr)
	}
}

func TestLoadUnitCSV(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "unitTags.csv")
	if err := os.WriteFile(path, []byte("1001,Engine 1\nbad,row\n1002,Medic 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadUnitCSV(path)
	if err != nil {
		t.Fatalf("LoadUnitCSV() error = %v", err)
	}
	want := []UnitEntry{{1001, "Engine 1"}, {1002, "Medic 2"}}
	if !reflect.DeepEqual(got.Entries, want) || got.Skipped != 1 {
		t.Errorf("LoadUnitCSV() = %#v (skipped %d), want %#v (skipped 1)", got.Entries, got.Skipped, want)
	}

	if _, err := LoadUnitCSV(filepath.Join(t.TempDir(), "missing.csv")); err == nil {
		t.Error("LoadUnitCSV(missing) error = nil, want error")
	}
}

func TestParseTalkgroupCSVDetailed_BOM(t *testing.T) {
	t.Parallel()

	input := "\ufeffDecimal,Hex,Alpha Tag,Mode,Description,Tag,Category\n100,64,Fire Dispatch,D,County Fire,Fire Dispatch,Fire\n"
	got, err := ParseTalkgroupCSVDetailed(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseTalkgroupCSVDetailed() error = %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Tgid != 100 || got.Entries[0].AlphaTag != "Fire Dispatch" {
		t.Errorf("Entries = %#v, want one entry for tgid 100 / Fire Dispatch", got.Entries)
	}
}

func TestDiscover_UnitTags(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	unitPath := filepath.Join(dir, "units.csv")
	cfg, _ := json.Marshal(map[string]any{
		"captureDir": filepath.Join(dir, "audio"),
		"systems": []map[string]string{
			{"shortName": "butco", "type": "p25", "unitTagsFile": unitPath},
		},
	})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("\ufeffUnit ID,Alpha Tag\n1001,Engine 1\nx,y\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Discover(dir, zerolog.Nop())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Systems) != 1 {
		t.Fatalf("systems = %d, want 1", len(result.Systems))
	}
	sys := result.Systems[0]
	if want := []UnitEntry{{1001, "Engine 1"}}; !reflect.DeepEqual(sys.Units, want) {
		t.Errorf("Units = %#v, want %#v", sys.Units, want)
	}
	if sys.UnitCSVPath != unitPath {
		t.Errorf("UnitCSVPath = %q, want %q", sys.UnitCSVPath, unitPath)
	}
}
