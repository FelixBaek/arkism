package skills

import (
	"fmt"
	"sort"
	"strings"
)

// A line-for-line port of Python difflib's SequenceMatcher (no isjunk, autojunk on) and
// unified_diff, so review diffs read exactly as before.

type opcode struct {
	tag            byte // 'e'qual, 'r'eplace, 'd'elete, 'i'nsert
	i1, i2, j1, j2 int
}

type matcher struct {
	a, b []string
	b2j  map[string][]int
}

func newMatcher(a, b []string) *matcher {
	m := &matcher{a: a, b: b, b2j: map[string][]int{}}
	for i, s := range b {
		m.b2j[s] = append(m.b2j[s], i)
	}
	if n := len(b); n >= 200 {
		ntest := n/100 + 1
		for s, idx := range m.b2j {
			if len(idx) > ntest {
				delete(m.b2j, s)
			}
		}
	}
	return m
}

func (m *matcher) longest(alo, ahi, blo, bhi int) (int, int, int) {
	besti, bestj, best := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		next := map[int]int{}
		for _, j := range m.b2j[m.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			next[j] = k
			if k > best {
				besti, bestj, best = i-k+1, j-k+1, k
			}
		}
		j2len = next
	}
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti, bestj, best = besti-1, bestj-1, best+1
	}
	for besti+best < ahi && bestj+best < bhi && m.a[besti+best] == m.b[bestj+best] {
		best++
	}
	return besti, bestj, best
}

func (m *matcher) blocks() [][3]int {
	queue := [][4]int{{0, len(m.a), 0, len(m.b)}}
	var found [][3]int
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		alo, ahi, blo, bhi := q[0], q[1], q[2], q[3]
		i, j, k := m.longest(alo, ahi, blo, bhi)
		if k > 0 {
			found = append(found, [3]int{i, j, k})
			if alo < i && blo < j {
				queue = append(queue, [4]int{alo, i, blo, j})
			}
			if i+k < ahi && j+k < bhi {
				queue = append(queue, [4]int{i + k, ahi, j + k, bhi})
			}
		}
	}
	sort.Slice(found, func(x, y int) bool {
		a, b := found[x], found[y]
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		if a[1] != b[1] {
			return a[1] < b[1]
		}
		return a[2] < b[2]
	})
	var out [][3]int
	i1, j1, k1 := 0, 0, 0
	for _, f := range found {
		if i1+k1 == f[0] && j1+k1 == f[1] {
			k1 += f[2]
			continue
		}
		if k1 > 0 {
			out = append(out, [3]int{i1, j1, k1})
		}
		i1, j1, k1 = f[0], f[1], f[2]
	}
	if k1 > 0 {
		out = append(out, [3]int{i1, j1, k1})
	}
	return append(out, [3]int{len(m.a), len(m.b), 0})
}

func (m *matcher) opcodes() []opcode {
	var out []opcode
	i, j := 0, 0
	for _, blk := range m.blocks() {
		ai, bj, size := blk[0], blk[1], blk[2]
		var tag byte
		switch {
		case i < ai && j < bj:
			tag = 'r'
		case i < ai:
			tag = 'd'
		case j < bj:
			tag = 'i'
		}
		if tag != 0 {
			out = append(out, opcode{tag, i, ai, j, bj})
		}
		i, j = ai+size, bj+size
		if size > 0 {
			out = append(out, opcode{'e', ai, i, bj, j})
		}
	}
	return out
}

func (m *matcher) grouped(n int) [][]opcode {
	codes := m.opcodes()
	if len(codes) == 0 {
		codes = []opcode{{'e', 0, 1, 0, 1}}
	}
	if c := &codes[0]; c.tag == 'e' {
		c.i1, c.j1 = max(c.i1, c.i2-n), max(c.j1, c.j2-n)
	}
	if c := &codes[len(codes)-1]; c.tag == 'e' {
		c.i2, c.j2 = min(c.i2, c.i1+n), min(c.j2, c.j1+n)
	}
	var groups [][]opcode
	var group []opcode
	for _, c := range codes {
		if c.tag == 'e' && c.i2-c.i1 > 2*n {
			group = append(group, opcode{'e', c.i1, min(c.i2, c.i1+n), c.j1, min(c.j2, c.j1+n)})
			groups = append(groups, group)
			group = nil
			c.i1, c.j1 = max(c.i1, c.i2-n), max(c.j1, c.j2-n)
		}
		group = append(group, c)
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].tag == 'e') {
		groups = append(groups, group)
	}
	return groups
}

func unifiedRange(start, stop int) string {
	begin, length := start+1, stop-start
	if length == 1 {
		return fmt.Sprint(begin)
	}
	if length == 0 {
		begin--
	}
	return fmt.Sprintf("%d,%d", begin, length)
}

// unifiedDiff is "".join(difflib.unified_diff(a, b, from, to, n=n)) over lines that keep their ends.
func unifiedDiff(a, b []string, from, to string, n int) string {
	var out strings.Builder
	for gi, group := range newMatcher(a, b).grouped(n) {
		if gi == 0 {
			fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
		}
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", unifiedRange(first.i1, last.i2), unifiedRange(first.j1, last.j2))
		for _, c := range group {
			if c.tag == 'e' {
				for _, l := range a[c.i1:c.i2] {
					out.WriteString(" " + l)
				}
				continue
			}
			if c.tag == 'r' || c.tag == 'd' {
				for _, l := range a[c.i1:c.i2] {
					out.WriteString("-" + l)
				}
			}
			if c.tag == 'r' || c.tag == 'i' {
				for _, l := range b[c.j1:c.j2] {
					out.WriteString("+" + l)
				}
			}
		}
	}
	return out.String()
}
