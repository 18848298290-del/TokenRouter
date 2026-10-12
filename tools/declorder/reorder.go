package main

import (
	"bytes"
	"fmt"
	"go/token"
	"slices"
	"strings"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

// iotaNolint exempts additional iota blocks from decorder's block count check.
const iotaNolint = "//nolint:decorder // Separate const blocks keep each iota sequence starting at zero."

// reorder 按 import、const、var、type、func 的顺序重排一个 Go 文件的顶层声明。
//
// 同类声明保持原来的相对顺序，var 的初始化顺序因此不变。分散的 const 和 var
// 各合并成一个括号块，原来的文档注释移到块内对应的第一条声明上。func 里 init
// 排在最前，构造函数移到同一类型的第一个方法前面，规则和 golangci-lint 的
// decorder、funcorder 一致。返回值 notes 列出需要人工看一眼的位置。
func reorder(src []byte) (out []byte, notes []string, err error) {
	fset := token.NewFileSet()
	dec := decorator.NewDecorator(fset)
	file, err := dec.Parse(src)
	if err != nil {
		return nil, nil, err
	}

	var imports, consts, vars, types []*dst.GenDecl
	var funcs []*dst.FuncDecl
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *dst.FuncDecl:
			funcs = append(funcs, d)
		case *dst.GenDecl:
			switch d.Tok {
			case token.IMPORT:
				imports = append(imports, d)
			case token.CONST:
				consts = append(consts, d)
			case token.VAR:
				vars = append(vars, d)
			case token.TYPE:
				types = append(types, d)
			}
		}
	}

	notes = append(notes, detachedComments(consts)...)
	notes = append(notes, detachedComments(vars)...)

	bare := bareDecls(append(append([]*dst.GenDecl{}, consts...), vars...), dec, fset)
	constDecls := mergeConsts(consts, bare)

	decls := make([]dst.Decl, 0, len(file.Decls))
	for _, d := range imports {
		decls = append(decls, d)
	}
	for _, d := range constDecls {
		decls = append(decls, d)
	}
	if len(vars) > 0 {
		decls = append(decls, mergeGenDecls(vars, bare))
	}
	for _, d := range types {
		decls = append(decls, d)
	}
	for _, d := range orderFuncs(funcs, typeNames(types)) {
		decls = append(decls, d)
	}

	// 相邻关系变了的声明前面空一行。相邻关系没变的保留原来的间距，
	// 连写在一起的单行方法仍然连在一起。
	prev := make(map[dst.Decl]dst.Decl, len(file.Decls))
	for i := 1; i < len(file.Decls); i++ {
		prev[file.Decls[i]] = file.Decls[i-1]
	}
	if len(decls) > 0 && decls[0] != file.Decls[0] {
		decls[0].Decorations().Before = dst.EmptyLine
	}
	for i := 1; i < len(decls); i++ {
		if prev[decls[i]] != decls[i-1] {
			decls[i].Decorations().Before = dst.EmptyLine
			decls[i-1].Decorations().After = dst.None
		}
	}
	file.Decls = decls

	var buf bytes.Buffer
	if err := decorator.Fprint(&buf, file); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), notes, nil
}

// mergeConsts 把 const 声明合并成一个块。
//
// iota 的值等于它在块内的序号，含 iota 的块放在合并结果的最前面，取值不变。
// 一个文件有多个含 iota 的块时，第二个起保持独立，并加上 iotaNolint。
func mergeConsts(decls []*dst.GenDecl, bare map[*dst.GenDecl]bool) []*dst.GenDecl {
	if len(decls) == 0 {
		return nil
	}
	var iotaDecls, plainDecls []*dst.GenDecl
	for _, d := range decls {
		if usesIota(d) {
			iotaDecls = append(iotaDecls, d)
		} else {
			plainDecls = append(plainDecls, d)
		}
	}
	if len(iotaDecls) == 0 {
		return []*dst.GenDecl{mergeGenDecls(plainDecls, bare)}
	}

	merged := mergeGenDecls(append([]*dst.GenDecl{iotaDecls[0]}, plainDecls...), bare)
	for _, d := range iotaDecls[1:] {
		if !slices.Contains(d.Decs.Start, iotaNolint) {
			d.Decs.Start.Append(iotaNolint)
		}
	}
	return append([]*dst.GenDecl{merged}, iotaDecls[1:]...)
}

// mergeGenDecls 把同一种 GenDecl 合并成一个括号块，只有一个声明时原样返回。
//
// 原来的每个声明在块内自成一组，组之间空一行。相邻两组都在 bare 里时连写，
// 一串没有注释的单行声明合并后排在一起，由 gofmt 对齐。
func mergeGenDecls(decls []*dst.GenDecl, bare map[*dst.GenDecl]bool) *dst.GenDecl {
	if len(decls) == 1 {
		return decls[0]
	}
	merged := &dst.GenDecl{
		Tok:    decls[0].Tok,
		Lparen: true,
		Rparen: true,
	}
	for i, d := range decls {
		specs := d.Specs
		first := specs[0].(*dst.ValueSpec)
		last := specs[len(specs)-1].(*dst.ValueSpec)

		// 声明的文档注释和 "(" 后面的注释，移到块内这一组的第一条声明上。
		start := append(dst.Decorations{}, d.Decs.Start...)
		start = append(start, d.Decs.Tok...)
		start = append(start, d.Decs.Lparen...)
		first.Decs.Start = append(start, first.Decs.Start...)
		// 单行声明的行尾注释挂在 GenDecl 上，移到最后一条声明，仍然留在同一行。
		last.Decs.End = append(last.Decs.End, d.Decs.End...)

		if i == 0 || bare[d] && bare[decls[i-1]] {
			first.Decs.Before = dst.NewLine
		} else {
			first.Decs.Before = dst.EmptyLine
		}
		last.Decs.After = dst.NewLine
		merged.Specs = append(merged.Specs, specs...)
	}
	return merged
}

// bareDecls 找出没有注释、不带括号、只占一行的单条 const 或 var 声明。
func bareDecls(decls []*dst.GenDecl, dec *decorator.Decorator, fset *token.FileSet) map[*dst.GenDecl]bool {
	bare := make(map[*dst.GenDecl]bool)
	for _, d := range decls {
		if d.Lparen || len(d.Specs) != 1 || len(d.Decs.Start) > 0 || len(d.Decs.Tok) > 0 {
			continue
		}
		if len(d.Specs[0].(*dst.ValueSpec).Decs.Start) > 0 {
			continue
		}
		node := dec.Ast.Nodes[d]
		if fset.Position(node.Pos()).Line == fset.Position(node.End()).Line {
			bare[d] = true
		}
	}
	return bare
}

// orderFuncs 把 init 排在最前，再把构造函数移到所属类型的第一个方法前面。
func orderFuncs(funcs []*dst.FuncDecl, localTypes map[string]bool) []*dst.FuncDecl {
	var inits, others []*dst.FuncDecl
	for _, f := range funcs {
		if f.Recv == nil && f.Name.Name == "init" {
			inits = append(inits, f)
		} else {
			others = append(others, f)
		}
	}

	for _, ctor := range append([]*dst.FuncDecl{}, others...) {
		typ := constructorType(ctor)
		if typ == "" || !localTypes[typ] {
			continue
		}
		ctorIndex := indexOf(others, ctor)
		methodIndex := firstMethodIndex(others, typ)
		if methodIndex < 0 || methodIndex > ctorIndex {
			continue
		}
		others = append(others[:ctorIndex], others[ctorIndex+1:]...)
		others = append(others[:methodIndex], append([]*dst.FuncDecl{ctor}, others[methodIndex:]...)...)
	}
	return append(inits, others...)
}

// constructorType 按 funcorder 的规则识别构造函数，返回它构造的类型名。
//
// 构造函数是导出的普通函数，名字以 New 或 Must 开头（不区分大小写，且比前缀长），
// 第一个返回值是 T 或 *T。
func constructorType(f *dst.FuncDecl) string {
	if f.Recv != nil || !dst.IsExported(f.Name.Name) {
		return ""
	}
	if f.Type.Results == nil || len(f.Type.Results.List) == 0 {
		return ""
	}
	name := strings.ToLower(f.Name.Name)
	isConstructor := false
	for _, prefix := range []string{"new", "must"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			isConstructor = true
		}
	}
	if !isConstructor {
		return ""
	}
	return identName(f.Type.Results.List[0].Type)
}

// firstMethodIndex 返回类型 typ 的第一个方法在 funcs 里的下标，没有方法时返回 -1。
func firstMethodIndex(funcs []*dst.FuncDecl, typ string) int {
	for i, f := range funcs {
		if f.Recv != nil && len(f.Recv.List) == 1 && identName(f.Recv.List[0].Type) == typ {
			return i
		}
	}
	return -1
}

// identName 返回 T 或 *T 里的类型名。泛型等其他形式返回空字符串，和 funcorder 一样跳过。
func identName(expr dst.Expr) string {
	switch e := expr.(type) {
	case *dst.StarExpr:
		return identName(e.X)
	case *dst.Ident:
		return e.Name
	}
	return ""
}

func indexOf(funcs []*dst.FuncDecl, target *dst.FuncDecl) int {
	for i, f := range funcs {
		if f == target {
			return i
		}
	}
	return -1
}

// typeNames 收集文件里声明的类型名。
func typeNames(decls []*dst.GenDecl) map[string]bool {
	names := make(map[string]bool)
	for _, d := range decls {
		for _, spec := range d.Specs {
			names[spec.(*dst.TypeSpec).Name.Name] = true
		}
	}
	return names
}

// usesIota 判断 const 块里是否出现 iota。
func usesIota(d *dst.GenDecl) bool {
	found := false
	dst.Inspect(d, func(n dst.Node) bool {
		if id, ok := n.(*dst.Ident); ok && id.Name == "iota" && id.Path == "" {
			found = true
		}
		return !found
	})
	return found
}

// detachedComments 找出和声明之间隔着空行的注释。
//
// 这类注释通常是分节标题，合并后会跟着下一条声明移动，位置需要人工确认。
func detachedComments(decls []*dst.GenDecl) []string {
	if len(decls) < 2 {
		return nil
	}
	var notes []string
	for _, d := range decls {
		for _, c := range d.Decs.Start {
			if c == "\n" {
				notes = append(notes, fmt.Sprintf("%s 声明前有隔着空行的注释：%s", d.Tok, firstComment(d.Decs.Start)))
				break
			}
		}
	}
	return notes
}

func firstComment(decs dst.Decorations) string {
	for _, c := range decs {
		if c != "\n" {
			return c
		}
	}
	return ""
}
