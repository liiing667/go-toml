package toml_test

// Contract tests for the behaviors this project relies on when reading
// production configuration with go-toml. They pin down error shapes, encoding
// shapes, and round-trip guarantees so that upgrades of the library cannot
// silently change them.
//
// Known differences from naive expectations are marked with "KNOWN:" comments
// instead of being "fixed" here: these tests document actual behavior.

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/internal/assert"
)

// 1. Strict mode (DisallowUnknownFields) on nested tables, inline tables and
// arrays of tables: error type, key path, and position must locate the
// offending key.
func TestContractStrictMode(t *testing.T) {
	type inner struct {
		A int `toml:"a"`
	}

	examples := []struct {
		desc        string
		doc         string
		target      func() any
		expectedKey toml.Key
		expectedRow int
		expectedCol int
		// substring of the rendered error that identifies the bad key
		expectedHighlight string
	}{
		{
			desc: "nested table",
			doc:  "[inner]\nb = 1\n",
			target: func() any {
				return &struct {
					Inner inner `toml:"inner"`
				}{}
			},
			expectedKey:       toml.Key{"inner", "b"},
			expectedRow:       2,
			expectedCol:       1,
			expectedHighlight: "b = 1",
		},
		{
			desc: "inline table",
			doc:  "in = {b = 1}\n",
			target: func() any {
				return &struct {
					In inner `toml:"in"`
				}{}
			},
			// KNOWN: for inline tables the reported key path is not
			// prefixed with the parent key ("in"); only the position
			// (row 1, col 7) points at the offending key.
			expectedKey:       toml.Key{"b"},
			expectedRow:       1,
			expectedCol:       7,
			expectedHighlight: "b = 1",
		},
		{
			desc: "array of tables",
			doc:  "[[items]]\nb = 1\n",
			target: func() any {
				return &struct {
					Items []inner `toml:"items"`
				}{}
			},
			expectedKey:       toml.Key{"items", "b"},
			expectedRow:       2,
			expectedCol:       1,
			expectedHighlight: "b = 1",
		},
	}

	for _, e := range examples {
		e := e
		t.Run(e.desc, func(t *testing.T) {
			t.Run("unknown field is rejected and located", func(t *testing.T) {
				d := toml.NewDecoder(strings.NewReader(e.doc))
				d.DisallowUnknownFields()
				err := d.Decode(e.target())

				var sm *toml.StrictMissingError
				if !errors.As(err, &sm) {
					t.Fatalf("expected *toml.StrictMissingError, got %v (%T)", err, err)
				}
				assert.Equal(t, 1, len(sm.Errors))

				de := sm.Errors[0]
				assert.Equal(t, e.expectedKey, de.Key())
				row, col := de.Position()
				assert.Equal(t, e.expectedRow, row)
				assert.Equal(t, e.expectedCol, col)
				assert.Equal(t, "toml: unknown field", de.Error())

				rendered := sm.String()
				if !strings.Contains(rendered, e.expectedHighlight) {
					t.Fatalf("rendered error should contain %q, got:\n%s", e.expectedHighlight, rendered)
				}
			})

			t.Run("known fields are accepted", func(t *testing.T) {
				doc := strings.ReplaceAll(e.doc, "b = 1", "a = 1")
				d := toml.NewDecoder(strings.NewReader(doc))
				d.DisallowUnknownFields()
				assert.NoError(t, d.Decode(e.target()))
			})

			t.Run("unknown field ignored without strict mode", func(t *testing.T) {
				d := toml.NewDecoder(strings.NewReader(e.doc))
				assert.NoError(t, d.Decode(e.target()))
			})
		})
	}
}

// 2. Offset date-time, local date, local time and local date-time must survive
// a parse -> encode -> parse cycle unchanged.
func TestContractDateTimeRoundTrip(t *testing.T) {
	type config struct {
		Odt time.Time          `toml:"odt"`
		Ld  toml.LocalDate     `toml:"ld"`
		Lt  toml.LocalTime     `toml:"lt"`
		Ldt toml.LocalDateTime `toml:"ldt"`
	}
	doc := `
odt = 1979-05-27T07:32:00.999999-08:00
ld = 1979-05-27
lt = 07:32:00.5
ldt = 1979-05-27T07:32:00
`

	var first config
	assert.NoError(t, toml.Unmarshal([]byte(doc), &first))

	// Sanity: the offset is preserved, not normalized to UTC.
	_, offset := first.Odt.Zone()
	assert.Equal(t, -8*60*60, offset)
	assert.Equal(t, 500000000, first.Lt.Nanosecond)

	encoded, err := toml.Marshal(first)
	assert.NoError(t, err)

	// KNOWN: fractional seconds are emitted with the precision needed to
	// represent the value, not necessarily the original width.
	for _, fragment := range []string{
		"odt = 1979-05-27T07:32:00.999999-08:00",
		"ld = 1979-05-27",
		"lt = 07:32:00.5",
		"ldt = 1979-05-27T07:32:00",
	} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatalf("encoded document should contain %q, got:\n%s", fragment, encoded)
		}
	}

	var second config
	assert.NoError(t, toml.Unmarshal(encoded, &second))

	if !first.Odt.Equal(second.Odt) {
		t.Fatalf("offset date-time changed across round-trip: %v != %v", first.Odt, second.Odt)
	}
	_, offset2 := second.Odt.Zone()
	assert.Equal(t, offset, offset2)
	assert.Equal(t, first.Ld, second.Ld)
	assert.Equal(t, first.Lt, second.Lt)
	assert.Equal(t, first.Ldt, second.Ldt)

	t.Run("UTC offset date-time encodes with Z suffix", func(t *testing.T) {
		in := struct {
			T time.Time `toml:"t"`
		}{T: time.Date(1979, 5, 27, 7, 32, 0, 0, time.UTC)}
		out, err := toml.Marshal(in)
		assert.NoError(t, err)
		assert.Equal(t, "t = 1979-05-27T07:32:00Z\n", string(out))
	})

	t.Run("impossible date is rejected", func(t *testing.T) {
		var v struct {
			D toml.LocalDate `toml:"d"`
		}
		err := toml.Unmarshal([]byte("d = 1979-02-30\n"), &v)
		assert.Error(t, err)
		if !strings.Contains(err.Error(), "impossible date") {
			t.Fatalf("error should mention impossible date, got: %v", err)
		}
	})

	t.Run("garbage date-time is rejected", func(t *testing.T) {
		var v struct {
			T time.Time `toml:"t"`
		}
		assert.Error(t, toml.Unmarshal([]byte(`t = "not a date"`+"\n"), &v))
	})
}

// 3. Marshal output shape for empty arrays, empty inline tables and tables
// that would only carry comments.
func TestContractEmptyShapes(t *testing.T) {
	type empty struct{}

	type config struct {
		EmptyArr    []int            `toml:"empty_arr"`
		EmptyInline empty            `toml:"empty_inline,inline"`
		EmptyMap    map[string]int   `toml:"empty_map"`
		CommentOnly empty            `toml:"comment_only"`
		Nested      map[string]empty `toml:"nested"`
	}

	out, err := toml.Marshal(config{
		EmptyArr: []int{},
		EmptyMap: map[string]int{},
		Nested:   map[string]empty{"child": {}},
	})
	assert.NoError(t, err)

	// KNOWN: an empty map encodes as a bare table header ([empty_map]),
	// not as an inline empty table (empty_map = {}).
	// KNOWN: intermediate maps emit their own header ([nested]) even when
	// they hold no direct values.
	expected := `empty_arr = []
empty_inline = {}

[empty_map]
[comment_only]
[nested]
[nested.child]
`
	assert.Equal(t, expected, string(out))

	t.Run("empty shapes decode back", func(t *testing.T) {
		var decoded struct {
			EmptyArr []int `toml:"empty_arr"`
		}
		assert.NoError(t, toml.Unmarshal([]byte("empty_arr = []\n"), &decoded))
		assert.Equal(t, 0, len(decoded.EmptyArr))
	})

	t.Run("comment-only document decodes to zero value", func(t *testing.T) {
		var decoded struct {
			A int `toml:"a"`
		}
		assert.NoError(t, toml.Unmarshal([]byte("# nothing but a comment\n"), &decoded))
		assert.Equal(t, 0, decoded.A)
	})

	t.Run("nil slice keeps the same shape as empty slice", func(t *testing.T) {
		out, err := toml.Marshal(struct {
			A []int `toml:"a"`
		}{})
		assert.NoError(t, err)
		assert.Equal(t, "a = []\n", string(out))
	})
}

// 4. Integer and float boundaries: int64 extremes, NaN and +/-Inf.
func TestContractNumberBoundaries(t *testing.T) {
	t.Run("int64 extremes round-trip", func(t *testing.T) {
		type config struct {
			Max int64 `toml:"max"`
			Min int64 `toml:"min"`
		}
		in := config{Max: math.MaxInt64, Min: math.MinInt64}
		encoded, err := toml.Marshal(in)
		assert.NoError(t, err)
		assert.Equal(t, "max = 9223372036854775807\nmin = -9223372036854775808\n", string(encoded))

		var out config
		assert.NoError(t, toml.Unmarshal(encoded, &out))
		assert.Equal(t, in, out)
	})

	t.Run("NaN and Inf round-trip", func(t *testing.T) {
		type config struct {
			Nan  float64 `toml:"nan"`
			Pinf float64 `toml:"pinf"`
			Ninf float64 `toml:"ninf"`
		}
		in := config{Nan: math.NaN(), Pinf: math.Inf(1), Ninf: math.Inf(-1)}
		encoded, err := toml.Marshal(in)
		assert.NoError(t, err)
		assert.Equal(t, "nan = nan\npinf = inf\nninf = -inf\n", string(encoded))

		var out config
		assert.NoError(t, toml.Unmarshal(encoded, &out))
		assert.True(t, math.IsNaN(out.Nan))
		assert.True(t, math.IsInf(out.Pinf, 1))
		assert.True(t, math.IsInf(out.Ninf, -1))
	})

	t.Run("integer literal beyond int64 is rejected", func(t *testing.T) {
		var v struct {
			N int64 `toml:"n"`
		}
		err := toml.Unmarshal([]byte("n = 9223372036854775808\n"), &v)
		assert.Error(t, err)
		if !strings.Contains(err.Error(), "too large") {
			t.Fatalf("error should mention the value is too large, got: %v", err)
		}
	})

	t.Run("integer overflowing target type is rejected", func(t *testing.T) {
		var v struct {
			N int8 `toml:"n"`
		}
		err := toml.Unmarshal([]byte("n = 200\n"), &v)
		assert.Error(t, err)
		if !strings.Contains(err.Error(), "cannot be stored in int8") {
			t.Fatalf("error should name the target type, got: %v", err)
		}
	})

	t.Run("negative int64 bound below range is rejected", func(t *testing.T) {
		var v struct {
			N int64 `toml:"n"`
		}
		assert.Error(t, toml.Unmarshal([]byte("n = -9223372036854775809\n"), &v))
	})
}

// 5. Marshal -> Unmarshal round-trip on a representative configuration struct
// must yield an equal structure (compared field by field, not as strings).
func TestContractRoundTrip(t *testing.T) {
	type server struct {
		Host string   `toml:"host"`
		Port uint16   `toml:"port"`
		Tags []string `toml:"tags"`
	}
	type limits struct {
		Ratio float64 `toml:"ratio"`
		Burst int64   `toml:"burst"`
	}
	type config struct {
		Name    string            `toml:"name"`
		Enabled bool              `toml:"enabled"`
		When    time.Time         `toml:"when"`
		Day     toml.LocalDate    `toml:"day"`
		Servers []server          `toml:"servers"`
		Limits  limits            `toml:"limits"`
		Labels  map[string]string `toml:"labels"`
	}

	in := config{
		Name:    "api",
		Enabled: true,
		When:    time.Date(2026, 9, 29, 12, 30, 0, 123456000, time.FixedZone("CST", 8*60*60)),
		Day:     toml.LocalDate{Year: 2026, Month: 9, Day: 29},
		Servers: []server{
			{Host: "a.example.com", Port: 8080, Tags: []string{"blue", "green"}},
			{Host: "b.example.com", Port: 9090, Tags: []string{}},
		},
		Limits: limits{Ratio: 0.75, Burst: math.MinInt64},
		Labels: map[string]string{"env": "prod", "team": "sre"},
	}

	encoded, err := toml.Marshal(in)
	assert.NoError(t, err)

	var out config
	assert.NoError(t, toml.Unmarshal(encoded, &out))

	assert.Equal(t, in.Name, out.Name)
	assert.Equal(t, in.Enabled, out.Enabled)
	if !in.When.Equal(out.When) {
		t.Fatalf("time changed across round-trip: %v != %v", in.When, out.When)
	}
	_, inOffset := in.When.Zone()
	_, outOffset := out.When.Zone()
	assert.Equal(t, inOffset, outOffset)
	assert.Equal(t, in.Day, out.Day)
	assert.Equal(t, in.Servers, out.Servers)
	assert.Equal(t, in.Limits, out.Limits)
	assert.Equal(t, in.Labels, out.Labels)

	t.Run("double round-trip is stable", func(t *testing.T) {
		encoded2, err := toml.Marshal(out)
		assert.NoError(t, err)
		assert.Equal(t, string(encoded), string(encoded2))
	})

	t.Run("round-trip with NaN compares structurally", func(t *testing.T) {
		type floats struct {
			Values []float64 `toml:"values"`
		}
		in := floats{Values: []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -0.0}}
		encoded, err := toml.Marshal(in)
		assert.NoError(t, err)

		var out floats
		assert.NoError(t, toml.Unmarshal(encoded, &out))
		assert.Equal(t, len(in.Values), len(out.Values))
		for i := range in.Values {
			want, got := in.Values[i], out.Values[i]
			same := want == got ||
				(math.IsNaN(want) && math.IsNaN(got)) ||
				(math.IsInf(want, 1) && math.IsInf(got, 1)) ||
				(math.IsInf(want, -1) && math.IsInf(got, -1))
			assert.True(t, same, "index %d: %v != %v", i, want, got)
		}
	})

	t.Run("corrupted document fails to decode", func(t *testing.T) {
		var out config
		corrupted := bytes.Replace(encoded, []byte("enabled = true"), []byte("enabled = not-a-bool"), 1)
		assert.Error(t, toml.Unmarshal(corrupted, &out))
	})
}
