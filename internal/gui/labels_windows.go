package gui

import "github.com/lxn/walk"

// setShort shows a shortened text; the full text goes to the tooltip.
func setShort(l *walk.Label, text string, n int) {
	short := Shorten(text, n)
	if l.Text() != short {
		l.SetText(short)
	}
	tip := ""
	if short != text {
		tip = text
	}
	if l.ToolTipText() != tip {
		l.SetToolTipText(tip)
	}
}

// setShortTail is setShort keeping the end of the text (file paths).
func setShortTail(l *walk.Label, text string, n int) {
	short := ShortenTail(text, n)
	if l.Text() != short {
		l.SetText(short)
	}
	tip := ""
	if short != text {
		tip = text
	}
	if l.ToolTipText() != tip {
		l.SetToolTipText(tip)
	}
}
