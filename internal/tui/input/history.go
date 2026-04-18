package input

type History struct {
	entries []string
	idx     int
	temp    string
}

func NewHistory() *History {
	return &History{
		entries: make([]string, 0),
		idx:     0,
	}
}

func (h *History) Push(entry string) {
	if entry == "" {
		return
	}
	// Don't push duplicate of last entry
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == entry {
		h.idx = len(h.entries)
		h.temp = ""
		return
	}
	h.entries = append(h.entries, entry)
	h.idx = len(h.entries)
	h.temp = ""
}

func (h *History) Prev(current string) (string, bool) {
	if h.idx == len(h.entries) {
		h.temp = current
	}
	if h.idx > 0 {
		h.idx--
		return h.entries[h.idx], true
	}
	return "", false
}

func (h *History) Next() (string, bool) {
	if h.idx < len(h.entries) {
		h.idx++
		if h.idx == len(h.entries) {
			return h.temp, true
		}
		return h.entries[h.idx], true
	}
	return "", false
}

func (h *History) ResetIndex() {
	h.idx = len(h.entries)
	h.temp = ""
}
