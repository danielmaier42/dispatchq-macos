package wslproto

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestFramesRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	frames := []struct {
		typ byte
		p   []byte
	}{{FrameHeader, []byte(`{"v":1}`)}, {FrameData, []byte("C\x00{\"op\":\"cancel\"}\n")}, {FrameEnd, nil}, {FramePing, nil}, {FrameCancel, nil}}
	for _, f := range frames {
		if err := WriteFrame(&buf, f.typ, f.p); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range frames {
		typ, p, err := ReadFrame(&buf)
		if err != nil || typ != f.typ || !bytes.Equal(p, f.p) {
			t.Fatalf("got %c %q %v, want %c %q", typ, p, err, f.typ, f.p)
		}
	}
	if _, _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v, want io.EOF", err)
	}
}

func TestMalformedFrames(t *testing.T) {
	for name, raw := range map[string][]byte{
		"unknown type":   {'X', 0, 0, 0, 0},
		"short header":   {'D', 0},
		"short payload":  {'D', 0, 0, 0, 5, 'a'},
		"oversized":      {'D', 0xff, 0xff, 0xff, 0xff},
		"raw json input": []byte(`{"op":"cancel"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ReadFrame(bytes.NewReader(raw)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
}
