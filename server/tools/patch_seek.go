package tools

import "unicode"

// seekSequence searches for a line pattern and returns the start index.
func seekSequence(lines []string, pattern []string, start int, endOfFile bool) int {
	if len(pattern) == 0 {
		return start
	}
	if len(pattern) > len(lines) {
		return -1
	}

	searchStart := start
	if endOfFile && len(lines) >= len(pattern) {
		searchStart = len(lines) - len(pattern)
	}

	if idx := matchSequence(lines, pattern, searchStart, identityMatch); idx >= 0 {
		return idx
	}
	if idx := matchSequence(lines, pattern, searchStart, trimRightMatch); idx >= 0 {
		return idx
	}
	if idx := matchSequence(lines, pattern, searchStart, trimMatch); idx >= 0 {
		return idx
	}
	if idx := matchSequence(lines, pattern, searchStart, normalizeMatch); idx >= 0 {
		return idx
	}
	return -1
}

type normalizeFunc func(string) string

// matchSequence finds a sequence using the provided normalization.
func matchSequence(lines []string, pattern []string, start int, norm normalizeFunc) int {
	max := len(lines) - len(pattern)
	for i := start; i <= max; i++ {
		matched := true
		for j, p := range pattern {
			if norm(lines[i+j]) != norm(p) {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

// identityMatch returns the original string.
func identityMatch(s string) string {
	return s
}

// trimRightMatch trims trailing whitespace.
func trimRightMatch(s string) string {
	return trimRightSpace(s)
}

// trimMatch trims leading and trailing whitespace.
func trimMatch(s string) string {
	return trimSpace(s)
}

// normalizeMatch applies normalization.
func normalizeMatch(s string) string {
	return normalizeForMatch(s)
}

// trimRightSpace trims trailing whitespace runes.
func trimRightSpace(s string) string {
	return string(trimRightRunes([]rune(s)))
}

// trimSpace trims leading and trailing whitespace runes.
func trimSpace(s string) string {
	return string(trimRunes([]rune(s)))
}

// trimRightRunes trims trailing whitespace runes.
func trimRightRunes(runes []rune) []rune {
	end := len(runes)
	for end > 0 && unicode.IsSpace(runes[end-1]) {
		end--
	}
	return runes[:end]
}

// trimRunes trims leading and trailing whitespace runes.
func trimRunes(runes []rune) []rune {
	start := 0
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	end := len(runes)
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	return runes[start:end]
}

// normalizeForMatch normalizes common Unicode punctuation to ASCII.
func normalizeForMatch(s string) string {
	trimmed := trimSpace(s)
	out := make([]rune, 0, len(trimmed))
	for _, r := range trimmed {
		switch r {
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			r = '-'
		case '\u2018', '\u2019', '\u201A', '\u201B':
			r = '\''
		case '\u201C', '\u201D', '\u201E', '\u201F':
			r = '"'
		case '\u00A0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000':
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
