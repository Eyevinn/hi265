package decoder

import (
	"reflect"
	"testing"
)

// TestRebaseEntryPoints covers the translation from raw NAL byte offsets to
// offsets in the emulation-prevention-stripped slice data. Spec 7.4.7.1 counts
// emulation prevention bytes towards these offsets, so on content that escapes
// often the raw values run past the end of the stripped buffer.
func TestRebaseEntryPoints(t *testing.T) {
	cases := []struct {
		name        string
		entryPoints []int
		dropped     []int
		want        []int
	}{
		{"nothing escaped", []int{10, 20, 30}, nil, []int{10, 20, 30}},
		{"one escape before all", []int{10, 20}, []int{5}, []int{9, 19}},
		{"escapes interleaved", []int{10, 20, 30}, []int{5, 15, 25}, []int{9, 18, 27}},
		{"escape after the last", []int{10}, []int{50}, []int{10}},
		{"escape exactly at the offset", []int{10}, []int{10}, []int{10}},
		{"no entry points", nil, []int{5}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rebaseEntryPoints(c.entryPoints, c.dropped)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("rebaseEntryPoints(%v, %v) = %v, want %v",
					c.entryPoints, c.dropped, got, c.want)
			}
		})
	}
}

// TestRemoveEmulationPreventionBytesWithMap checks the stripped output and the
// reported positions together, since the offsets depend on both.
func TestRemoveEmulationPreventionBytesWithMap(t *testing.T) {
	in := []byte{0x00, 0x00, 0x03, 0x01, 0xff, 0x00, 0x00, 0x03, 0x02}
	wantOut := []byte{0x00, 0x00, 0x01, 0xff, 0x00, 0x00, 0x02}
	wantDropped := []int{2, 7}

	out, dropped := removeEmulationPreventionBytesWithMap(in)
	if !reflect.DeepEqual(out, wantOut) {
		t.Errorf("stripped = % x, want % x", out, wantOut)
	}
	if !reflect.DeepEqual(dropped, wantDropped) {
		t.Errorf("dropped = %v, want %v", dropped, wantDropped)
	}
	// The stripped length must equal the input minus the drops.
	if len(out) != len(in)-len(dropped) {
		t.Errorf("stripped %d bytes from %d input with %d drops",
			len(out), len(in), len(dropped))
	}
}
