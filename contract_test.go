package toml_test

// Contract tests guarding the behaviors our production configuration loading
// depends on. These tests intentionally assert exact error positions, exact
// marshal output shapes, and structural (not string) round-trip equality.
// Where the library's actual behavior deviates from what one might naively
// expect, the assertion documents the real behavior and is marked as a
// "known difference" in a comment.

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// --- 1. strict mode (DisallowUnknownFields) --------------------------------

func strictDecode(t *testing.T, doc string, target any) *toml.StrictMissingError {
	t.Helper()
	err := toml.NewDecoder(strings.NewReader(doc)).DisallowUnknownFields().Decode(target)
	if err == nil {
		t.Fatalf("expected a strict mode error, got nil")
	}
	var sme *toml.StrictMissingError
	if !errors.As(err, &sme) {
		t.Fatalf("expected a *toml.StrictMissingError, got %T: %v", err, err)
	}
	return sme
}

func TestContractStrictModeNestedTable(t *testing.T) {
	type Config struct {
		Server struct {
			Host string
		}
	}
	doc := "[server]\nhost = \"x\"\nunknown = 1\n"

	sme := strictDecode(t, doc, &Config{})
	if len(sme.Errors) != 1 {
		t.Fatalf("expected 1 missing-field error, got %d", len(sme.Errors))
	}

	e := sme.Errors[0]
	if got, want := e.Key(), (toml.Key{"server", "unknown"}); !reflect.DeepEqual(got, want) {
		t.Errorf("error key = %q, want %q", got, want)
	}
	row, col := e.Position()
	if row != 3 || col != 1 {
		t.Errorf("error position = (%d, %d), want (3, 1)", row, col)
	}
	if !strings.Contains(e.Error(), "unknown field") {
		t.Errorf("error message %q does not mention unknown field", e.Error())
	}
	// The human-readable rendering must point at the offending line.
	if !strings.Contains(sme.String(), "unknown = 1") {
		t.Errorf("human-readable error does not show the offending line:\n%s", sme.String())
	}
}

func TestContractStrictModeInlineTable(t *testing.T) {
	type Config struct {
		Point struct {
			X int
			Y int
		}
	}
	doc := "point = {x = 1, y = 2, z = 3}\n"

	sme := strictDecode(t, doc, &Config{})
	if len(sme.Errors) != 1 {
		t.Fatalf("expected 1 missing-field error, got %d", len(sme.Errors))
	}

	e := sme.Errors[0]
	// Known difference: for inline tables the reported key is only the
	// offending leaf ("z"); the parent path ("point") is not included.
	if got, want := e.Key(), (toml.Key{"z"}); !reflect.DeepEqual(got, want) {
		t.Errorf("error key = %q, want %q", got, want)
	}
	row, _ := e.Position()
	if row != 1 {
		t.Errorf("error row = %d, want 1", row)
	}
	if !strings.Contains(sme.String(), "z = 3") {
		t.Errorf("human-readable error does not show the offending entry:\n%s", sme.String())
	}
}

func TestContractStrictModeArrayOfTables(t *testing.T) {
	type Config struct {
		Items []struct {
			Name string
		}
	}
	doc := "[[items]]\nname = \"a\"\nextra = true\n[[items]]\nname = \"b\"\n"

	sme := strictDecode(t, doc, &Config{})
	if len(sme.Errors) != 1 {
		t.Fatalf("expected 1 missing-field error, got %d", len(sme.Errors))
	}

	e := sme.Errors[0]
	if got, want := e.Key(), (toml.Key{"items", "extra"}); !reflect.DeepEqual(got, want) {
		t.Errorf("error key = %q, want %q", got, want)
	}
	row, col := e.Position()
	if row != 3 || col != 1 {
		t.Errorf("error position = (%d, %d), want (3, 1)", row, col)
	}
}

func TestContractStrictModeValidDocumentPasses(t *testing.T) {
	type Config struct {
		Server struct {
			Host string
			Port int
		}
		Items []struct {
			Name string
		}
	}
	doc := "[server]\nhost = \"x\"\nport = 8080\n[[items]]\nname = \"a\"\n"

	var cfg Config
	err := toml.NewDecoder(strings.NewReader(doc)).DisallowUnknownFields().Decode(&cfg)
	if err != nil {
		t.Fatalf("valid document rejected in strict mode: %v", err)
	}
	if cfg.Server.Host != "x" || cfg.Server.Port != 8080 || len(cfg.Items) != 1 || cfg.Items[0].Name != "a" {
		t.Errorf("decoded value mismatch: %+v", cfg)
	}
}

// --- 2. date/time parse + re-encode equivalence ----------------------------

type timeConfig struct {
	Odt time.Time
	Ld  toml.LocalDate
	Lt  toml.LocalTime
	Ldt toml.LocalDateTime
}

func TestContractDateTimeRoundTrip(t *testing.T) {
	doc := "odt = 2024-03-01T12:30:45.123456789+08:00\n" +
		"ld = 2024-02-29\n" +
		"lt = 23:59:59.5\n" +
		"ldt = 2024-03-01T12:30:45.999\n"

	var first timeConfig
	if err := toml.Unmarshal([]byte(doc), &first); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Sanity-check the parsed values carry the expected zone and fractions.
	if _, offset := first.Odt.Zone(); offset != 8*60*60 {
		t.Errorf("offset date-time zone offset = %d, want %d", offset, 8*60*60)
	}
	if first.Odt.Nanosecond() != 123456789 {
		t.Errorf("offset date-time nanoseconds = %d, want 123456789", first.Odt.Nanosecond())
	}

	encoded, err := toml.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var second timeConfig
	if err := toml.Unmarshal(encoded, &second); err != nil {
		t.Fatalf("re-unmarshal of %q: %v", encoded, err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("date/time round trip not equal:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

func TestContractInvalidDateTimeRejected(t *testing.T) {
	docs := map[string]string{
		"non-leap-year feb 29": "ld = 2023-02-29\n",
		"hour out of range":    "lt = 25:00:00\n",
		"month out of range":   "ld = 2024-13-01\n",
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			var v struct {
				Ld toml.LocalDate
				Lt toml.LocalTime
			}
			if err := toml.Unmarshal([]byte(doc), &v); err == nil {
				t.Fatalf("expected an error for %q, got nil", doc)
			}
		})
	}
}

// --- 3. marshal output shape of empty values --------------------------------

func TestContractMarshalEmptyShapes(t *testing.T) {
	type Config struct {
		EmptyArr   []int
		EmptyMap   map[string]string
		EmptyInner struct{}
		NonEmpty   []int
	}
	cfg := Config{
		EmptyArr: []int{},
		EmptyMap: map[string]string{},
		NonEmpty: []int{1},
	}

	out, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	// Empty slices encode as empty inline arrays; empty maps and empty
	// structs encode as bare table headers.
	for _, want := range []string{
		"EmptyArr = []\n",
		"NonEmpty = [1]\n",
		"[EmptyMap]\n",
		"[EmptyInner]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("marshal output missing %q:\n%s", want, got)
		}
	}
}

func TestContractMarshalNilShapes(t *testing.T) {
	type Config struct {
		A []int
		M map[string]int
	}

	out, err := toml.Marshal(Config{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "A = []\n") {
		t.Errorf("nil slice should encode as empty array, got:\n%s", got)
	}
	// Known difference: a nil map is omitted entirely, while a non-nil but
	// empty map produces a bare table header.
	if strings.Contains(got, "[M]") {
		t.Errorf("nil map should be omitted, got:\n%s", got)
	}
}

func TestContractCommentOnlyTable(t *testing.T) {
	// A table whose body is only comments decodes to an empty struct and
	// re-encodes as a bare table header (comments are not preserved).
	type Config struct {
		Meta struct{}
	}

	var cfg Config
	if err := toml.Unmarshal([]byte("[meta]\n# only a comment here\n"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(out), "[Meta]\n"; got != want {
		t.Errorf("marshal output = %q, want %q", got, want)
	}
}

// --- 4. integer and float boundaries ---------------------------------------

func TestContractInt64BoundsRoundTrip(t *testing.T) {
	type Config struct {
		Max int64
		Min int64
	}
	in := Config{Max: math.MaxInt64, Min: math.MinInt64}

	encoded, err := toml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), "Max = 9223372036854775807\n") ||
		!strings.Contains(string(encoded), "Min = -9223372036854775808\n") {
		t.Errorf("unexpected encoding of int64 bounds:\n%s", encoded)
	}

	var out Config
	if err := toml.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("int64 bounds round trip = %+v, want %+v", out, in)
	}
}

func TestContractIntegerOverflowRejected(t *testing.T) {
	docs := map[string]string{
		"above max int64": "x = 9223372036854775808\n",
		"below min int64": "x = -9223372036854775809\n",
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			var v struct{ X int64 }
			err := toml.Unmarshal([]byte(doc), &v)
			if err == nil {
				t.Fatalf("expected an overflow error for %q, got nil", doc)
			}
			if !strings.Contains(err.Error(), "too large to fit in a 64-bit signed integer") {
				t.Errorf("overflow error = %q, want mention of 64-bit signed integer", err.Error())
			}
		})
	}
}

func TestContractUint64AboveMaxInt64CannotMarshal(t *testing.T) {
	_, err := toml.Marshal(map[string]any{"u": uint64(math.MaxUint64)})
	if err == nil {
		t.Fatal("expected an error marshaling uint64 above math.MaxInt64, got nil")
	}
	if !strings.Contains(err.Error(), "cannot encode an unsigned integer above math.MaxInt64") {
		t.Errorf("error = %q, want mention of MaxInt64", err.Error())
	}
}

func TestContractNonFiniteFloats(t *testing.T) {
	type Config struct {
		Nan  float64
		Pinf float64
		Ninf float64
	}
	in := Config{Nan: math.NaN(), Pinf: math.Inf(1), Ninf: math.Inf(-1)}

	encoded, err := toml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{"Nan = nan\n", "Pinf = inf\n", "Ninf = -inf\n"} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("marshal output missing %q:\n%s", want, encoded)
		}
	}

	var out Config
	if err := toml.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Known difference: NaN != NaN, so non-finite floats cannot be compared
	// with == or reflect.DeepEqual; check them with math helpers instead.
	if !math.IsNaN(out.Nan) {
		t.Errorf("Nan round trip = %v, want NaN", out.Nan)
	}
	if !math.IsInf(out.Pinf, 1) {
		t.Errorf("Pinf round trip = %v, want +Inf", out.Pinf)
	}
	if !math.IsInf(out.Ninf, -1) {
		t.Errorf("Ninf round trip = %v, want -Inf", out.Ninf)
	}
}

func TestContractFloatOverflowRejected(t *testing.T) {
	var v struct{ X float64 }
	err := toml.Unmarshal([]byte("x = 1e999\n"), &v)
	if err == nil {
		t.Fatal("expected an error for 1e999, got nil")
	}
	if !strings.Contains(err.Error(), "value out of range") {
		t.Errorf("error = %q, want mention of out of range", err.Error())
	}
}

// --- 5. marshal/unmarshal round-trip equivalence ----------------------------

type roundTripInner struct {
	Name  string
	Tags  []string
	Ratio float64
}

type roundTripConfig struct {
	Title   string
	Enabled bool
	Count   int64
	Inner   roundTripInner
	Inners  []roundTripInner
	Limits  map[string]int64
	When    time.Time
	Due     toml.LocalDate
}

func TestContractMarshalUnmarshalRoundTrip(t *testing.T) {
	in := roundTripConfig{
		Title:   "service \"api\" 配置",
		Enabled: true,
		Count:   -42,
		Inner: roundTripInner{
			Name:  "primary",
			Tags:  []string{"a", "b"},
			Ratio: 1.5,
		},
		Inners: []roundTripInner{
			{Name: "x", Tags: []string{}, Ratio: 0},
			{Name: "y", Tags: []string{"c"}, Ratio: -2.75},
		},
		Limits: map[string]int64{"cpu": 4, "mem": 8192},
		When:   time.Date(2024, 3, 1, 12, 30, 45, 123456789, time.FixedZone("CST", 8*60*60)),
		Due:    toml.LocalDate{Year: 2024, Month: 2, Day: 29},
	}

	encoded, err := toml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out roundTripConfig
	if err := toml.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal of %q: %v", encoded, err)
	}

	// Known difference: the zone name of a time.Time ("CST") is not
	// preserved through TOML, only the numeric offset is. Compare the
	// instant and the offset, then zero the field before DeepEqual.
	if !in.When.Equal(out.When) {
		t.Errorf("round trip instant = %v, want %v", out.When, in.When)
	}
	_, inOffset := in.When.Zone()
	_, outOffset := out.When.Zone()
	if inOffset != outOffset {
		t.Errorf("round trip zone offset = %d, want %d", outOffset, inOffset)
	}
	in.When, out.When = time.Time{}, time.Time{}

	// Structural equality, not string equality: the re-decoded value must be
	// identical to the original.
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestContractRoundTripNilSliceBecomesEmpty(t *testing.T) {
	// Known difference: a nil slice marshals as an empty array and decodes
	// back as a non-nil empty slice, so DeepEqual fails across the round
	// trip. Code comparing configs after reload must treat nil and empty
	// slices as equivalent.
	type Config struct {
		Tags []string
	}

	encoded, err := toml.Marshal(Config{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out Config
	if err := toml.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Tags == nil {
		t.Fatal("expected nil slice to decode as non-nil empty slice")
	}
	if len(out.Tags) != 0 {
		t.Errorf("expected empty slice, got %v", out.Tags)
	}
}

// Ensure the strict-mode decoder is usable through bytes buffers too, which
// is how the production loader feeds it.
func TestContractStrictModeDecoderFromBuffer(t *testing.T) {
	var cfg struct {
		Known string
	}
	doc := []byte("known = \"v\"\nsurprise = 2\n")

	err := toml.NewDecoder(bytes.NewReader(doc)).DisallowUnknownFields().Decode(&cfg)
	if err == nil {
		t.Fatal("expected strict mode error, got nil")
	}
	var sme *toml.StrictMissingError
	if !errors.As(err, &sme) {
		t.Fatalf("expected *toml.StrictMissingError, got %T", err)
	}
	if len(sme.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(sme.Errors))
	}
	if got, want := sme.Errors[0].Key(), (toml.Key{"surprise"}); !reflect.DeepEqual(got, want) {
		t.Errorf("error key = %q, want %q", got, want)
	}
}
