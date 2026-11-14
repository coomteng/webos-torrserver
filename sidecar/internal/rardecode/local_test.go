package rardecode

import (
	"errors"
	"hash/crc32"
	"testing"
)

// Tests of the local additions (see README.md).

func TestCustomFilterRefused(t *testing.T) {
	code := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07}
	c := crc32.ChecksumIEEE(code)
	for _, f := range standardV3Filters {
		if f.crc == c && f.len == len(code) {
			t.Fatal("the sample program is a standard filter")
		}
	}
	if f, err := getV3Filter(code); !errors.Is(err, ErrCustomFilter) || f != nil {
		t.Fatalf("got filter %v, err %v", f != nil, err)
	}
}

func TestPPMdModelReused(t *testing.T) {
	var a subAllocator
	a.init(2)
	first := &a.states[0]
	a.init(2)
	if &a.states[0] != first {
		t.Fatal("a second model of the same size was allocated anew")
	}
	a.init(1)
	if &a.states[0] != first {
		t.Fatal("a smaller model was allocated anew")
	}
}
