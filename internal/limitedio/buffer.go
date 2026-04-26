package limitedio

import "bytes"

// Buffer is an io.Writer that keeps only the first max bytes while reporting
// successful writes to avoid blocking command pipelines on output overflow.
type Buffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func NewBuffer(max int) *Buffer {
	if max < 0 {
		max = 0
	}
	return &Buffer{max: max}
}

func (b *Buffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	remaining := b.max - b.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	if remaining < len(p) {
		b.truncated = true
	}
	return len(p), nil
}

func (b *Buffer) Len() int {
	return b.buf.Len()
}

func (b *Buffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *Buffer) String() string {
	return b.buf.String()
}

func (b *Buffer) Truncated() bool {
	return b.truncated
}
