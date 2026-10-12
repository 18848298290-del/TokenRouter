package main

import (
	"strings"
	"testing"
)

func TestReorder_MovesDeclarationsAndMergesBlocks(t *testing.T) {
	src := `package sample

import "errors"

// ErrA 是第一个错误。
var ErrA = errors.New("a")

func Use() error { return ErrB }

// limit 是上限。
const limit = 3 // 行尾注释

// Item 是一个条目。
type Item struct{}

// 默认值。
const (
	// first 是第一个默认值。
	first = 1
	second = 2
)

var ErrB = errors.New("b")
`
	want := `package sample

import "errors"

const (
	// limit 是上限。
	limit = 3 // 行尾注释

	// 默认值。
	// first 是第一个默认值。
	first  = 1
	second = 2
)

var (
	// ErrA 是第一个错误。
	ErrA = errors.New("a")

	ErrB = errors.New("b")
)

// Item 是一个条目。
type Item struct{}

func Use() error { return ErrB }
`
	assertReorder(t, src, want)
}

func TestReorder_JoinsBareDeclarations(t *testing.T) {
	src := `package sample

import "errors"

var ErrA = errors.New("a")

var ErrLonger = errors.New("longer")

// ErrC 有文档注释。
var ErrC = errors.New("c")
var ErrD = errors.New("d")

var table = map[string]int{
	"a": 1,
}
`
	want := `package sample

import "errors"

var (
	ErrA      = errors.New("a")
	ErrLonger = errors.New("longer")

	// ErrC 有文档注释。
	ErrC = errors.New("c")

	ErrD = errors.New("d")

	table = map[string]int{
		"a": 1,
	}
)
`
	assertReorder(t, src, want)
}

func TestReorder_KeepsIotaBlockFirst(t *testing.T) {
	src := `package sample

const name = "x"

const (
	A = iota
	B
)
`
	want := `package sample

const (
	A = iota
	B

	name = "x"
)
`
	assertReorder(t, src, want)
}

func TestReorder_MarksSecondIotaBlock(t *testing.T) {
	src := `package sample

const (
	A = iota
	B
)

// C 是第二个枚举。
const (
	C = iota
	D
)
`
	want := `package sample

const (
	A = iota
	B
)

// C 是第二个枚举。
//nolint:decorder // Separate const blocks keep each iota sequence starting at zero.
const (
	C = iota
	D
)
`
	assertReorder(t, src, want)
	assertReorder(t, want, want)
}

func TestReorder_MovesInitAndConstructor(t *testing.T) {
	src := `package sample

type Server struct{}

func (s *Server) Run() {}

func helper() {}

func NewServer() *Server { return &Server{} }

func init() {}
`
	want := `package sample

type Server struct{}

func init() {}

func NewServer() *Server { return &Server{} }

func (s *Server) Run() {}

func helper() {}
`
	assertReorder(t, src, want)
}

func TestReorder_KeepsAdjacentOneLineMethods(t *testing.T) {
	src := `package sample

type byName []string

func (s byName) Len() int           { return len(s) }
func (s byName) Less(i, j int) bool { return s[i] < s[j] }
func (s byName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
`
	assertReorder(t, src, src)
}

func TestReorder_ReportsDetachedComment(t *testing.T) {
	src := `package sample

const a = 1

// 分节标题

// b 是第二个值。
const b = 2
`
	_, notes, err := reorder([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "分节标题") {
		t.Fatalf("需要报告隔着空行的注释，得到 %q", notes)
	}
}

func TestReorder_IsIdempotent(t *testing.T) {
	src := `package sample

func Use() int { return limit }

const limit = 3

type Item struct{}
`
	once, _, err := reorder([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	twice, _, err := reorder(once)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Fatalf("第二次运行改变了结果：\n%s\n----\n%s", once, twice)
	}
}

func assertReorder(t *testing.T, src, want string) {
	t.Helper()
	out, _, err := reorder([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != want {
		t.Fatalf("结果不符：\n%s\n---- 期望 ----\n%s", out, want)
	}
}
