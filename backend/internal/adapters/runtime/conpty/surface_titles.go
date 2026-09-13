package conpty

// oscTitleFilter removes OSC 0/1/2 title payloads from passive capture, not PTY replay.
// x/ansi mistakes UTF-8 continuation byte 0x9c (e.g. Claude's ✳) for ST and
// paints the remaining title into cells: https://github.com/charmbracelet/x/issues/848.
// Titles cannot affect cells; other OSC commands (palette, hyperlinks) still can.
type oscTitleFilter struct {
	prefix        []byte
	inTitle       bool
	utf8Remaining int
}

func (f *oscTitleFilter) filter(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		if f.inTitle {
			if f.utf8Remaining > 0 && b >= 0x80 && b <= 0xbf {
				f.utf8Remaining--
				continue
			}
			f.utf8Remaining = 0
			switch {
			case b == '\a' || b == 0x18 || b == 0x1a || b == 0x9c:
				f.inTitle = false
				out = append(out, b)
			case b == 0x1b:
				// ESC ends the title. Keep it to parse ST (ESC \), another
				// title, or the next cursor/style command normally.
				f.inTitle = false
				f.prefix = append(f.prefix, b)
			case b >= 0xc2 && b <= 0xdf:
				f.utf8Remaining = 1
			case b >= 0xe0 && b <= 0xef:
				f.utf8Remaining = 2
			case b >= 0xf0 && b <= 0xf4:
				f.utf8Remaining = 3
			}
			continue
		}

		switch {
		case len(f.prefix) == 1 && b == ']':
			f.prefix = append(f.prefix, b)
			continue
		case len(f.prefix) == 2 && b >= '0' && b <= '2':
			f.prefix = append(f.prefix, b)
			continue
		case len(f.prefix) == 3 && b == ';':
			// Retain the envelope: its ESC cancels any preceding partial
			// escape in the emulator, and the terminator restores ground.
			out = append(out, f.prefix...)
			out = append(out, b)
			f.prefix = f.prefix[:0]
			f.inTitle = true
			continue
		}
		out = append(out, f.prefix...)
		f.prefix = f.prefix[:0]
		if b == 0x1b {
			f.prefix = append(f.prefix, b)
		} else {
			out = append(out, b)
		}
	}
	return out
}
