package protocol

import "testing"

func TestMapper(t *testing.T) {
	// "é" is 2 bytes, 1 UTF-16 unit; "😀" is 4 bytes, 2 UTF-16 units.
	text := "ab\r\né😀x\n\nlast"
	type pt struct {
		off       int
		line, u16 uint32
		u8, u32   uint32
	}
	for _, c := range []pt{
		{0, 0, 0, 0, 0},
		{2, 0, 2, 2, 2}, // before \r: end of line
		{3, 0, 2, 2, 2}, // inside the line break
		{4, 1, 0, 0, 0},
		{6, 1, 1, 2, 1},  // after é
		{10, 1, 3, 6, 2}, // after 😀
		{11, 1, 4, 7, 3}, // after x
		{12, 2, 0, 0, 0},
		{13, 3, 0, 0, 0},
		{17, 3, 4, 4, 4},
	} {
		for _, e := range []struct {
			enc  string
			char uint32
		}{{UTF16, c.u16}, {UTF8, c.u8}, {UTF32, c.u32}} {
			m := NewMapper(text, e.enc)
			got := m.Position(c.off)
			want := Position{c.line, e.char}
			if got != want {
				t.Errorf("%s Position(%d) = %v, want %v", e.enc, c.off, got, want)
			}
			if c.off == 3 {
				continue // not a position's offset
			}
			off, err := m.Offset(want)
			if err != nil || off != c.off {
				t.Errorf("%s Offset(%v) = %d, %v; want %d", e.enc, want, off, err, c.off)
			}
		}
	}
	m := NewMapper(text, UTF16)
	if off, _ := m.Offset(Position{0, 99}); off != 2 {
		t.Errorf("past the end of line 0: %d, want 2", off)
	}
	if off, _ := m.Offset(Position{99, 0}); off != len(text) {
		t.Errorf("past the last line: %d, want %d", off, len(text))
	}
	if _, err := m.Offset(Position{1, 2}); err == nil {
		t.Error("inside a surrogate pair: no error")
	}
	if _, err := NewMapper(text, UTF8).Offset(Position{1, 1}); err == nil {
		t.Error("inside a UTF-8 sequence: no error")
	}
	if n := m.LineCount(); n != 4 {
		t.Errorf("LineCount = %d, want 4", n)
	}
}
