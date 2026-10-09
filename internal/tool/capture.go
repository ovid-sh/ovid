package tool

import "os"

// pipeCapture collects what a program writes to a pipe: the first limit bytes
// are kept and the rest only counted, so a program that prints without end
// costs no memory and no disk. The last tailSize bytes are kept too, for
// what a program says as it ends (the runtime's out-of-memory message).
type pipeCapture struct {
	w    *os.File // the program's end
	r    *os.File
	kept []byte
	tail []byte
	n    int64
	done chan struct{}
}

const tailSize = 64

func newPipeCapture(limit int) (*pipeCapture, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c := &pipeCapture{w: w, r: r, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		buf := make([]byte, 64<<10)
		for {
			k, err := r.Read(buf)
			if room := limit - len(c.kept); room > 0 {
				c.kept = append(c.kept, buf[:min(k, room)]...)
			}
			c.n += int64(k)
			c.tail = append(c.tail, buf[:k]...)
			if len(c.tail) > tailSize {
				c.tail = append(c.tail[:0], c.tail[len(c.tail)-tailSize:]...)
			}
			if err != nil {
				return
			}
		}
	}()
	return c, nil
}

// finish is called once the program has ended. It returns what was kept
// and how many bytes were written in all.
func (c *pipeCapture) finish() ([]byte, int64) {
	c.w.Close()
	<-c.done
	c.r.Close()
	return c.kept, c.n
}
