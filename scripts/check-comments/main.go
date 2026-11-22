// main.go：注释规范静态检查器——文件头、doc 贴附、二开标记配对、行号锚点与过期短语。
// 主要函数/类型：
//   - main / run：解析参数、遍历业务 Go 包与 web/src，输出分级结果并决定退出码。
//   - checkGoFile / checkTextFile：Go AST 与 TS/SQL 两条检查通道。
//   - checkGoHeader / checkGoDoc / pairMarkers：文件头、doc 贴附与二开标记检查。
//   - reporter：分级收集、排序并打印 path:line: [P0] message。

package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	sevP0   = "P0"
	sevP1   = "P1"
	sevP2   = "P2"
	sevNote = "NOTE"
)

// businessGoDirs 是方案附录 C 列出的 17 个业务 Go 包根目录。
var businessGoDirs = []string{
	"cmd", "server", "agent", "db", "guard", "intercept", "notify",
	"report", "evidence", "traffic", "xlog", "llmrec", "llmpool",
	"mcphttp", "sidequestion", "selfupdate", "enrich",
}

// denyPhrases 是已确认过期的注释/文案短语；P0，按文件行号报告。
var denyPhrases = []string{
	"60s ticker",
	"60s 轮询",
	"只靠 60s",
	"一律继承父任务",
	"硬继承父任务审核",
	"dual SQLite graph",
	"nullableInt64 returns",
}

var (
	markerRe     = regexp.MustCompile(`【二开·([^】]+)】\s*(起|止)`)
	anchorRe     = regexp.MustCompile(`\.(?:go|sql|tsx|ts):[0-9]+`)
	identWordRe  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)`)
	parenRe      = regexp.MustCompile(`[（(][^）)]*[）)]`)
	severityRank = map[string]int{sevP0: 0, sevP1: 1, sevP2: 2, sevNote: 3}
)

var severityOrder = []string{sevP0, sevP1, sevP2, sevNote}

// proseStops 避免把英文散文首词误判成符号名。
var proseStops = map[string]bool{
	"A": true, "An": true, "The": true, "This": true, "It": true,
	"Note": true, "See": true, "If": true, "Returns": true, "New": true,
	"Deprecated": true, "Example": true, "Package": true, "Usage": true,
	"TODO": true, "FIXME": true, "WARNING": true,
}

// docMismatchAllow 登记"首词恰与本文件某方法同名但实为散文"的已知误报，
// key = 相对路径:首词。新增条目必须在值里写明人工复核理由。
var docMismatchAllow = map[string]string{
	"intercept/intercept.go:Judge": "已人工复核：Judge 是 \"LLM fallback judge\" 功能的散文首词，const 块文档并非引用 Interceptor.Judge 方法",
}

type finding struct {
	path string
	line int
	sev  string
	msg  string
}

type reporter struct {
	findings []finding
	scanned  int
}

func (r *reporter) add(path string, line int, sev, format string, args ...any) {
	r.findings = append(r.findings, finding{
		path: path,
		line: line,
		sev:  sev,
		msg:  fmt.Sprintf(format, args...),
	})
}

func (r *reporter) counts() (p0, p1, p2, note int) {
	for _, f := range r.findings {
		switch f.sev {
		case sevP0:
			p0++
		case sevP1:
			p1++
		case sevP2:
			p2++
		default:
			note++
		}
	}
	return p0, p1, p2, note
}

func (r *reporter) print() {
	sort.SliceStable(r.findings, func(i, j int) bool {
		a, b := r.findings[i], r.findings[j]
		if a.path != b.path {
			return a.path < b.path
		}
		if a.line != b.line {
			return a.line < b.line
		}
		if severityRank[a.sev] != severityRank[b.sev] {
			return severityRank[a.sev] < severityRank[b.sev]
		}
		return a.msg < b.msg
	})
	for _, sev := range severityOrder {
		printed := false
		for _, f := range r.findings {
			if f.sev != sev {
				continue
			}
			if !printed {
				fmt.Printf("\n[%s]\n", sev)
				printed = true
			}
			fmt.Printf("%s:%d: [%s] %s\n", f.path, f.line, f.sev, f.msg)
		}
	}
	p0, p1, p2, note := r.counts()
	fmt.Printf("\n检查完成：文件 %d 个，P0=%d，P1=%d，P2=%d，NOTE=%d\n", r.scanned, p0, p1, p2, note)
}

func main() {
	root := flag.String("root", ".", "待检查的仓库根目录")
	warn := flag.Bool("warn", false, "警告模式：只报告，退出码保持 0")
	strict := flag.Bool("strict", false, "严格模式：配合 -warn 时仍按 P0/P1 返回失败")
	flag.Parse()
	os.Exit(run(*root, *warn, *strict))
}

func run(root string, warn, strict bool) int {
	root = filepath.Clean(root)
	rep := &reporter{}

	for _, dir := range businessGoDirs {
		walkFiles(root, dir, func(p string) bool {
			return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
		}, func(rel, abs string) {
			rep.scanned++
			checkGoFile(rep, rel, abs)
		})
	}

	walkFiles(root, filepath.Join("web", "src"), func(p string) bool {
		return strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")
	}, func(rel, abs string) {
		rep.scanned++
		checkTextFile(rep, rel, abs, "ts")
	})

	for _, dir := range []string{"db", "scripts"} {
		walkFiles(root, dir, func(p string) bool {
			return strings.HasSuffix(p, ".sql")
		}, func(rel, abs string) {
			rep.scanned++
			checkTextFile(rep, rel, abs, "sql")
		})
	}

	rep.print()
	if warn && !strict {
		return 0
	}
	p0, p1, _, _ := rep.counts()
	if p0 > 0 || p1 > 0 {
		return 1
	}
	return 0
}

func walkFiles(root, relDir string, want func(string) bool, fn func(rel, abs string)) {
	base := filepath.Join(root, relDir)
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if p != base && skipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() || !want(p) {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		fn(filepath.ToSlash(rel), p)
		return nil
	})
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".next", "dist", "vendor", "testdata":
		return true
	}
	return false
}

func checkGoFile(rep *reporter, rel, abs string) {
	data, err := os.ReadFile(abs)
	if err != nil {
		rep.add(rel, 1, sevP1, "读取失败：%v", err)
		return
	}
	lines := strings.Split(string(data), "\n")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, data, parser.ParseComments)
	if err != nil {
		rep.add(rel, 1, sevP1, "Go 解析失败：%v", err)
		return
	}
	declared := collectDeclared(f)
	checkGoHeader(rep, rel, abs, lines, f, fset, declared)
	checkGoDoc(rep, rel, fset, f, declared)
	checkGoComments(rep, rel, lines, f, fset)
	checkDenyList(rep, rel, lines)
}

func collectDeclared(f *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.FuncDecl:
			names[x.Name.Name] = true
			if x.Recv != nil && len(x.Recv.List) > 0 {
				if n := recvTypeName(x.Recv.List[0].Type); n != "" {
					names[n] = true
				}
			}
		case *ast.GenDecl:
			for _, s := range x.Specs {
				switch sp := s.(type) {
				case *ast.TypeSpec:
					names[sp.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						names[n.Name] = true
					}
				}
			}
		}
	}
	return names
}

func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	}
	return ""
}

func checkGoHeader(rep *reporter, rel, abs string, lines []string, f *ast.File, fset *token.FileSet, declared map[string]bool) {
	base := filepath.Base(abs)
	limit := 12
	if len(lines) < limit {
		limit = len(lines)
	}
	hasList := false
	headerLine := -1
	for i := range limit {
		if strings.Contains(lines[i], "主要函数/类型") {
			hasList = true
		}
		t := strings.TrimSpace(lines[i])
		if headerLine < 0 && (strings.HasPrefix(t, "// "+base+"：") || strings.HasPrefix(t, "// "+base+":")) {
			headerLine = i
		}
	}
	if !hasList {
		rep.add(rel, 1, sevP1, "缺少文件头：前 12 行未找到“主要函数/类型”清单")
	}

	buildTagFirst := len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "//go:build")
	if buildTagFirst {
		rep.add(rel, 1, sevNote, "首行为 build tag，跳过文件头首行格式检查")
	} else if headerLine != 0 {
		rep.add(rel, 1, sevP1, "文件头首行应为 // %s：<一句话职责>", base)
	}

	if headerLine < 0 {
		return
	}
	headerEnd := headerLine
	for headerEnd+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[headerEnd+1]), "//") {
		headerEnd++
	}

	pkgLine := fset.Position(f.Package).Line // 1-based
	if pkgLine >= 2 && headerEnd == pkgLine-2 {
		rep.add(rel, pkgLine, sevP1, "文件头与 package 之间缺少空行")
	}

	for i := headerLine; i <= headerEnd; i++ {
		part := headerSymbolPart(lines[i])
		if part == "" {
			continue
		}
		for _, sym := range splitSymbols(part) {
			if !isSymbolToken(sym) {
				continue
			}
			if !symbolDeclared(sym, declared) {
				rep.add(rel, i+1, sevP1, "头注释失名：%s 未在本文件声明", sym)
			}
		}
	}
}

func headerSymbolPart(line string) string {
	s := strings.TrimSpace(line)
	s = strings.TrimSpace(strings.TrimPrefix(s, "//"))
	if !strings.HasPrefix(s, "-") {
		return ""
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "-"))
	if i := strings.Index(s, "："); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, ":"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return ""
}

func splitSymbols(part string) []string {
	var out []string
	part = parenRe.ReplaceAllString(part, "")
	chunks := strings.FieldsFunc(part, func(r rune) bool {
		return r == '/' || r == '，' || r == '、' || r == ','
	})
	for _, chunk := range chunks {
		fields := strings.Fields(chunk)
		if len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out
}

func isSymbolToken(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '.' || r == '_' {
			continue
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func symbolDeclared(sym string, declared map[string]bool) bool {
	if declared[sym] {
		return true
	}
	if i := strings.Index(sym, "."); i > 0 && i < len(sym)-1 {
		return declared[sym[:i]] || declared[sym[i+1:]]
	}
	return false
}

func checkGoDoc(rep *reporter, rel string, fset *token.FileSet, f *ast.File, declared map[string]bool) {
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.FuncDecl:
			if x.Doc != nil {
				checkDocGroup(rep, rel, fset, x.Doc, []string{x.Name.Name}, declared)
			}
		case *ast.GenDecl:
			if x.Doc != nil {
				if names := specNames(x); len(names) > 0 {
					checkDocGroup(rep, rel, fset, x.Doc, names, declared)
				}
			}
			for _, spec := range x.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					if sp.Doc != nil {
						checkDocGroup(rep, rel, fset, sp.Doc, []string{sp.Name.Name}, declared)
					}
				case *ast.ValueSpec:
					if sp.Doc != nil && len(sp.Names) > 0 {
						names := make([]string, 0, len(sp.Names))
						for _, n := range sp.Names {
							names = append(names, n.Name)
						}
						checkDocGroup(rep, rel, fset, sp.Doc, names, declared)
					}
				}
			}
		}
	}
}

func specNames(d *ast.GenDecl) []string {
	var names []string
	for _, s := range d.Specs {
		switch sp := s.(type) {
		case *ast.TypeSpec:
			names = append(names, sp.Name.Name)
		case *ast.ValueSpec:
			for _, n := range sp.Names {
				names = append(names, n.Name)
			}
		}
	}
	return names
}

func checkDocGroup(rep *reporter, rel string, fset *token.FileSet, g *ast.CommentGroup, attached []string, declared map[string]bool) {
	if g == nil || len(g.List) == 0 {
		return
	}
	if isDirective(g.List[0].Text) {
		return
	}
	first := firstIdentifier(g.List[0].Text)
	if first == "" {
		return
	}
	for _, name := range attached {
		if first == name {
			return
		}
	}
	// 只在首词是本文件声明的另一个符号时报错位（plan 附录 C 的检测口径）；
	// 英文散文首词（Task/Preserve/Applied…）不是符号名，不判错位。
	if !declared[first] {
		return
	}
	if proseStops[first] {
		return
	}
	if _, ok := docMismatchAllow[rel+":"+first]; ok {
		return
	}
	rep.add(rel, fset.Position(g.Pos()).Line, sevP0, "doc 注释错位：首词 %s 与声明 %s 不一致", first, strings.Join(attached, " / "))
}

func firstIdentifier(text string) string {
	s := commentBody(text)
	if m := identWordRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func isTitleWord(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	if r < 'A' || r > 'Z' {
		return false
	}
	return s != strings.ToUpper(s)
}

func checkGoComments(rep *reporter, rel string, lines []string, f *ast.File, fset *token.FileSet) {
	var markers []marker
	for _, group := range f.Comments {
		for _, c := range group.List {
			line := fset.Position(c.Slash).Line
			if !isDirective(c.Text) {
				checkAnchor(rep, rel, line, c.Text)
				checkInlineLength(rep, rel, lines, line, fset.Position(c.Slash).Column, c.Text)
				markers = append(markers, extractMarkers(c.Text, line)...)
			}
		}
	}
	pairMarkers(rep, rel, markers)
}

func checkInlineLength(rep *reporter, rel string, lines []string, line, col int, text string) {
	if !strings.HasPrefix(text, "//") || isDirective(text) {
		return
	}
	if line < 1 || line > len(lines) || col <= 1 {
		return
	}
	full := strings.TrimRight(lines[line-1], "\r")
	prefix := full
	if col-1 < len(prefix) {
		prefix = full[:col-1]
	}
	if strings.TrimSpace(prefix) == "" {
		return
	}
	n := utf8.RuneCountInString(full)
	switch {
	case n > 150:
		rep.add(rel, line, sevP2, "行内注释整行 %d 字符（>150）", n)
	case n > 120:
		rep.add(rel, line, sevNote, "行内注释整行 %d 字符（>120）", n)
	}
}

func checkTextFile(rep *reporter, rel, abs, kind string) {
	data, err := os.ReadFile(abs)
	if err != nil {
		rep.add(rel, 1, sevP1, "读取失败：%v", err)
		return
	}
	lines := strings.Split(string(data), "\n")
	if kind == "ts" {
		checkTSHeader(rep, rel, abs, lines)
	}

	var markers []marker
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimRight(raw, "\r")
		idx, comment := findLineComment(line, kind)
		trimmed := strings.TrimSpace(line)
		hadComment := idx >= 0
		if hadComment && !isDirective(comment) {
			checkAnchor(rep, rel, lineNo, comment)
			if idx > 0 && strings.TrimSpace(line[:idx]) != "" {
				n := utf8.RuneCountInString(line)
				if n > 150 {
					rep.add(rel, lineNo, sevP2, "行内注释整行 %d 字符（>150）", n)
				} else if n > 120 {
					rep.add(rel, lineNo, sevNote, "行内注释整行 %d 字符（>120）", n)
				}
			}
		}
		if !hadComment && (strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*")) {
			checkAnchor(rep, rel, lineNo, trimmed)
			hadComment = true
		}
		if hadComment {
			if idx >= 0 {
				if !isDirective(comment) {
					markers = append(markers, extractMarkers(comment, lineNo)...)
				}
			} else {
				markers = append(markers, extractMarkers(line, lineNo)...)
			}
		}
	}
	pairMarkers(rep, rel, markers)
	checkDenyList(rep, rel, lines)
}

func checkTSHeader(rep *reporter, rel, abs string, lines []string) {
	base := filepath.Base(abs)
	limit := 10
	if len(lines) < limit {
		limit = len(lines)
	}
	found := -1
	for i := range limit {
		if strings.Contains(lines[i], base+"：") || strings.Contains(lines[i], "职责") || strings.Contains(lines[i], "主要函数/类型") {
			found = i
			break
		}
	}
	if found < 0 {
		rep.add(rel, 1, sevP1, "缺少文件头：前 10 行未找到“%s：…”职责说明", base)
		return
	}
	first := strings.TrimSpace(lines[0])
	if isUseClient(first) {
		j := 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || !isCommentLine(strings.TrimSpace(lines[j])) {
			rep.add(rel, 1, sevP1, `"use client" 之后应紧跟头注释块`)
		}
		return
	}
	if !isCommentLine(first) {
		rep.add(rel, 1, sevP1, `文件头应以块注释或 "use client" 开头`)
	}
}

func isUseClient(s string) bool {
	s = strings.TrimSpace(s)
	return s == `"use client";` || s == `"use client"` || s == `'use client';` || s == `'use client'`
}

func isCommentLine(s string) bool {
	return strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/*") || strings.HasPrefix(s, "*")
}

func findLineComment(line, kind string) (int, string) {
	var quote byte
	escaped := false
	for i := range len(line) {
		c := line[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' && quote != '`' {
				escaped = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			continue
		}
		if kind == "sql" {
			if c == '-' && i+1 < len(line) && line[i+1] == '-' {
				return i, line[i:]
			}
			if c == '/' && i+1 < len(line) && line[i+1] == '*' {
				return i, line[i:]
			}
			continue
		}
		if c == '/' && i+1 < len(line) {
			if line[i+1] == '/' || line[i+1] == '*' {
				return i, line[i:]
			}
		}
	}
	return -1, ""
}

type marker struct {
	id   string
	kind string
	line int
}

func extractMarkers(text string, line int) []marker {
	var out []marker
	for _, m := range markerRe.FindAllStringSubmatch(text, -1) {
		id := strings.TrimSpace(m[1])
		if fields := strings.Fields(id); len(fields) > 0 {
			id = fields[0] // 短功能名只作说明，配对按 <域>·<编号>。
		}
		if id == "" {
			continue
		}
		out = append(out, marker{id: id, kind: m[2], line: line})
	}
	return out
}

func pairMarkers(rep *reporter, rel string, markers []marker) {
	starts := map[string][]int{}
	var order []string
	for _, m := range markers {
		if m.kind == "起" {
			if _, ok := starts[m.id]; !ok {
				order = append(order, m.id)
			}
			starts[m.id] = append(starts[m.id], m.line)
			continue
		}
		stack := starts[m.id]
		if len(stack) == 0 {
			rep.add(rel, m.line, sevP1, "二开标记不配对：%s 的“止”没有对应“起”", m.id)
			continue
		}
		start := stack[len(stack)-1]
		starts[m.id] = stack[:len(stack)-1]
		span := m.line - start
		switch {
		case span <= 1:
			rep.add(rel, m.line, sevP1, "二开标记空区间：%s（起 %d，止 %d）", m.id, start, m.line)
		case span > 500:
			rep.add(rel, m.line, sevP1, "二开标记区间 %d 行（>500）：%s（起 %d）", span, m.id, start)
		case span > 80:
			rep.add(rel, m.line, sevP2, "二开标记区间 %d 行（>80）：%s（起 %d）", span, m.id, start)
		}
	}
	var leftover []marker
	for _, id := range order {
		for _, line := range starts[id] {
			leftover = append(leftover, marker{id: id, line: line})
		}
	}
	sort.Slice(leftover, func(i, j int) bool { return leftover[i].line < leftover[j].line })
	for _, m := range leftover {
		rep.add(rel, m.line, sevP1, "二开标记不配对：%s 的“起”没有对应“止”", m.id)
	}
}

func checkAnchor(rep *reporter, rel string, line int, text string) {
	if isDirective(text) {
		return
	}
	if m := anchorRe.FindString(text); m != "" {
		rep.add(rel, line, sevP1, "注释使用行号锚点 %s，改用 文件#符号", m)
	}
}

func checkDenyList(rep *reporter, rel string, lines []string) {
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if isDirective(line) {
			continue
		}
		for _, phrase := range denyPhrases {
			if strings.Contains(line, phrase) {
				rep.add(rel, i+1, sevP0, "过期字符串命中 deny-list：%q", phrase)
			}
		}
	}
}

func commentBody(text string) string {
	s := strings.TrimSpace(text)
	s = strings.TrimPrefix(s, "//")
	s = strings.TrimPrefix(s, "--")
	s = strings.TrimPrefix(s, "/*")
	s = strings.TrimSuffix(s, "*/")
	s = strings.TrimSpace(strings.TrimPrefix(s, "*"))
	return strings.TrimSpace(s)
}

func isDirective(text string) bool {
	b := commentBody(text)
	return strings.HasPrefix(b, "go:") || strings.HasPrefix(b, "nolint") || strings.HasPrefix(b, "biome-ignore")
}
