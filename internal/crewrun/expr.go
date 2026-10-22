package crewrun

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Condition expressions (condition node, config "expr"):
//
//	in.tests.failed == 0 and (in.status == "ok" or not in.retry)
//
// Paths start with "in" and walk object keys or list indexes ("in.items.0").
// Literals: numbers, 'single' or "double" quoted strings, true, false, null.
// Operators: == != > < >= <=, and or not (also && || !), parentheses.
// A missing path is null. A bare value is true unless it is null, false, 0,
// "" or an empty list/object. Comparing different types with == is false;
// ordering needs two numbers or two strings. There is no eval and no call.
type expr interface{ eval(in any) (any, error) }

type exprLit struct{ v any }
type exprPath []string
type exprNot struct{ x expr }
type exprBin struct {
	op   string
	l, r expr
}

func (e exprLit) eval(any) (any, error) { return e.v, nil }
func (e exprPath) eval(in any) (any, error) {
	return lookupPath(in, e), nil
}
func (e exprNot) eval(in any) (any, error) {
	v, err := e.x.eval(in)
	return !truthy(v), err
}
func (e exprBin) eval(in any) (any, error) {
	l, err := e.l.eval(in)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case "and":
		if !truthy(l) {
			return false, nil
		}
		r, err := e.r.eval(in)
		return truthy(r), err
	case "or":
		if truthy(l) {
			return true, nil
		}
		r, err := e.r.eval(in)
		return truthy(r), err
	}
	r, err := e.r.eval(in)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case "==":
		return equalJSON(l, r), nil
	case "!=":
		return !equalJSON(l, r), nil
	}
	if a, ok := l.(float64); ok {
		if b, ok := r.(float64); ok {
			return compare(e.op, a < b, a > b), nil
		}
	}
	if a, ok := l.(string); ok {
		if b, ok := r.(string); ok {
			return compare(e.op, a < b, a > b), nil
		}
	}
	return nil, fmt.Errorf("%s needs two numbers or two strings", e.op)
}

func compare(op string, less, more bool) bool {
	switch op {
	case "<":
		return less
	case ">":
		return more
	case "<=":
		return !more
	default: // ">="
		return !less
	}
}

func equalJSON(a, b any) bool {
	switch a.(type) {
	case nil, bool, float64, string:
		return a == b
	}
	return fmt.Sprint(a) == fmt.Sprint(b) && fmt.Sprintf("%T", a) == fmt.Sprintf("%T", b)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// lookupPath walks keys of decoded JSON; the leading "in" is optional.
func lookupPath(v any, path []string) any {
	if len(path) > 0 && path[0] == "in" {
		path = path[1:]
	}
	return walkPath(v, path)
}

// walkPath walks keys of decoded JSON exactly as given ("in" is a key like any other).
func walkPath(v any, path []string) any {
	for _, k := range path {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func splitPath(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

// EvalCondition parses and evaluates src against the decoded input.
func EvalCondition(src string, in any) (bool, error) {
	e, err := ParseExpr(src)
	if err != nil {
		return false, err
	}
	v, err := e.eval(in)
	return truthy(v), err
}

type token struct {
	kind string // num str ident op ( ) end
	text string
	num  float64
}

func lex(src string) ([]token, error) {
	var out []token
	rs := []rune(src)
	for i := 0; i < len(rs); {
		c := rs[i]
		var t token
		var err error
		switch {
		case unicode.IsSpace(c):
			i++
			continue
		case c == '(' || c == ')':
			t, i = token{kind: string(c)}, i+1
		case c == '"' || c == '\'':
			t, i, err = lexString(rs, i)
		case unicode.IsDigit(c) || c == '-' && i+1 < len(rs) && unicode.IsDigit(rs[i+1]):
			t, i, err = lexNumber(rs, i)
		case unicode.IsLetter(c) || c == '_':
			t, i = lexIdent(rs, i)
		default:
			t, i, err = lexOp(rs, i)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return append(out, token{kind: "end"}), nil
}

// lexString reads the quoted string at rs[i]; it returns the token and the index after it.
func lexString(rs []rune, i int) (token, int, error) {
	c := rs[i]
	j := i + 1
	var sb strings.Builder
	for ; j < len(rs) && rs[j] != c; j++ {
		if rs[j] == '\\' && j+1 < len(rs) {
			j++
		}
		sb.WriteRune(rs[j])
	}
	if j >= len(rs) {
		return token{}, j, fmt.Errorf("unclosed string")
	}
	return token{kind: "str", text: sb.String()}, j + 1, nil
}

func lexNumber(rs []rune, i int) (token, int, error) {
	j := i + 1
	for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
		j++
	}
	n, err := strconv.ParseFloat(string(rs[i:j]), 64)
	if err != nil {
		return token{}, j, fmt.Errorf("bad number %q", string(rs[i:j]))
	}
	return token{kind: "num", num: n}, j, nil
}

func lexIdent(rs []rune, i int) (token, int) {
	j := i
	for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' || rs[j] == '.' || rs[j] == '-') {
		j++
	}
	return token{kind: "ident", text: string(rs[i:j])}, j
}

func lexOp(rs []rune, i int) (token, int, error) {
	c := rs[i]
	two := ""
	if i+1 < len(rs) {
		two = string(rs[i : i+2])
	}
	switch {
	case two == "==" || two == "!=" || two == ">=" || two == "<=":
		return token{kind: "op", text: two}, i + 2, nil
	case two == "&&":
		return token{kind: "ident", text: "and"}, i + 2, nil
	case two == "||":
		return token{kind: "ident", text: "or"}, i + 2, nil
	case c == '>' || c == '<':
		return token{kind: "op", text: string(c)}, i + 1, nil
	case c == '!':
		return token{kind: "ident", text: "not"}, i + 1, nil
	}
	return token{}, i, fmt.Errorf("unexpected %q", string(c))
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}
func (p *parser) word(w string) bool {
	t := p.peek()
	if t.kind == "ident" && t.text == w {
		p.next()
		return true
	}
	return false
}

// ParseExpr builds the expression tree; it never runs anything.
func ParseExpr(src string) (expr, error) {
	if len(src) > 2000 {
		return nil, fmt.Errorf("expression too long")
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	e, err := p.or(0)
	if err != nil {
		return nil, err
	}
	if p.peek().kind != "end" {
		return nil, fmt.Errorf("unexpected trailing input")
	}
	return e, nil
}

const maxExprDepth = 64

func (p *parser) or(d int) (expr, error) {
	l, err := p.and(d)
	for err == nil && p.word("or") {
		var r expr
		if r, err = p.and(d); err == nil {
			l = exprBin{"or", l, r}
		}
	}
	return l, err
}

func (p *parser) and(d int) (expr, error) {
	l, err := p.not(d)
	for err == nil && p.word("and") {
		var r expr
		if r, err = p.not(d); err == nil {
			l = exprBin{"and", l, r}
		}
	}
	return l, err
}

func (p *parser) not(d int) (expr, error) {
	if d > maxExprDepth {
		return nil, fmt.Errorf("expression too deep")
	}
	if p.word("not") {
		x, err := p.not(d + 1)
		return exprNot{x}, err
	}
	return p.cmp(d)
}

func (p *parser) cmp(d int) (expr, error) {
	l, err := p.primary(d)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind == "op" {
		p.next()
		r, err := p.primary(d)
		if err != nil {
			return nil, err
		}
		return exprBin{t.text, l, r}, nil
	}
	return l, nil
}

func (p *parser) primary(d int) (expr, error) {
	t := p.next()
	switch t.kind {
	case "num":
		return exprLit{t.num}, nil
	case "str":
		return exprLit{t.text}, nil
	case "(":
		e, err := p.or(d + 1)
		if err != nil {
			return nil, err
		}
		if p.next().kind != ")" {
			return nil, fmt.Errorf("missing )")
		}
		return e, nil
	case "ident":
		switch t.text {
		case "true":
			return exprLit{true}, nil
		case "false":
			return exprLit{false}, nil
		case "null":
			return exprLit{nil}, nil
		case "and", "or", "not":
			return nil, fmt.Errorf("unexpected %q", t.text)
		}
		if t.text != "in" && !strings.HasPrefix(t.text, "in.") {
			return nil, fmt.Errorf("paths start with in: %q", t.text)
		}
		return exprPath(splitPath(t.text)), nil
	}
	return nil, fmt.Errorf("unexpected token")
}
