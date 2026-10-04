package tool

import "fmt"

// PageLimit is how many records a paged command prints when no limit is
// given on the command line.
const PageLimit = 200

// Page selects the records a paged command prints: Offset of them are
// skipped, then at most Limit printed (Limit <= 0 means all the rest).
type Page struct {
	Offset, Limit int
}

// pager counts the records of one command against its Page.
type pager struct {
	Page
	total, shown int
}

// take counts one record and reports whether it falls on the page.
func (p *pager) take() bool {
	p.total++
	if p.total <= p.Offset || (p.Limit > 0 && p.shown >= p.Limit) {
		return false
	}
	p.shown++
	return true
}

// more is how many records lie past the page.
func (p *pager) more() int {
	if n := p.total - p.Offset - p.shown; n > 0 {
		return n
	}
	return 0
}

// finish adds the paging fields to a command's last line: count (records
// printed), total, offset, has_more, and next_offset when there is more.
func (p *pager) finish(r map[string]any) {
	r["count"], r["total"], r["offset"], r["has_more"] = p.shown, p.total, p.Offset, p.more() > 0
	if p.more() > 0 {
		r["next_offset"] = p.Offset + p.shown
		if r["hint"] == nil {
			r["hint"] = fmt.Sprintf("%d more; --offset %d for the next page", p.more(), p.Offset+p.shown)
		}
	}
}
