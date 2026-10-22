package ui

import "strings"

// displayText removes terminal instructions before untrusted text reaches a
// renderer. Application styles are added afterwards. Unterminated instructions
// consume the remainder, so their payload cannot become visible commands.
func displayText(s string) string {
	const (
		text = iota
		escape
		csi
		controlString
		stringEscape
	)
	state := text
	var b strings.Builder
	for _, r := range s {
		switch state {
		case controlString:
			if r == '\a' || r == '\u009c' {
				state = text
			} else if r == '\x1b' {
				state = stringEscape
			}
			continue
		case stringEscape:
			if r == '\\' || r == '\u009c' || r == '\a' {
				state = text
			} else {
				state = controlString
			}
			continue
		case csi:
			if r >= 0x40 && r <= 0x7e {
				state = text
			}
			continue
		case escape:
			switch r {
			case '[':
				state = csi
			case ']', 'P', 'X', '^', '_':
				state = controlString
			default:
				if r < 0x20 || r > 0x2f {
					state = text
				}
			}
			continue
		}
		switch r {
		case '\x1b':
			state = escape
		case '\u009b':
			state = csi
		case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
			state = controlString
		default:
			if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) ||
				r == '\u061c' || r == '\u200e' || r == '\u200f' || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Headers, rows and labels cannot introduce extra lines or tab alignment.
func displayLine(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(displayText(s))
}
