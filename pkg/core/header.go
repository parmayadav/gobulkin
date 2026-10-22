package core

import "strings"

const byteOrderMark = "\ufeff"

// Header finds columns by name, ignoring case, surrounding spaces and a leading BOM.
type Header struct {
	names []string
	index map[string]int
}

// NewHeader builds a Header. If a name repeats, the first column wins.
func NewHeader(names []string) *Header {
	h := &Header{names: make([]string, len(names)), index: make(map[string]int, len(names))}
	for i, name := range names {
		normalized := normalizeColumn(name)
		h.names[i] = normalized
		if normalized == "" {
			continue
		}
		if _, seen := h.index[normalized]; !seen {
			h.index[normalized] = i
		}
	}
	return h
}

// Names returns the normalised column names.
func (h *Header) Names() []string {
	if h == nil {
		return nil
	}
	return append([]string(nil), h.names...)
}

// Index returns the position of column.
func (h *Header) Index(column string) (int, bool) {
	if h == nil {
		return 0, false
	}
	i, ok := h.index[normalizeColumn(column)]
	return i, ok
}

// Missing returns the required columns the header lacks.
func (h *Header) Missing(required ...string) []string {
	var missing []string
	for _, column := range required {
		if _, ok := h.Index(column); !ok {
			missing = append(missing, column)
		}
	}
	return missing
}

func normalizeColumn(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, byteOrderMark)))
}
