// MiniDb - サンプル用の簡易データベース（Windows / macOS / Linux 対応）
// テーブルはデータフォルダ内の CSV ファイル（UTF-8）として保存される。
// データの編集は GUI（ブラウザで開く管理画面、gui.go）で行い、コマンドラインは内容の出力に使う。
// 結果は標準出力、メッセージとエラーは標準エラー出力に出すので、他システムからパイプで利用できる。
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
)

var version = "dev" // ビルド時に -ldflags "-X main.version=..." で上書き

type dbError struct{ msg string }

func (e dbError) Error() string { return e.msg }

func fail(format string, a ...interface{}) { panic(dbError{fmt.Sprintf(format, a...)}) }

type Table struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

func (t *Table) IndexOf(col string) int {
	for i, c := range t.Columns {
		if strings.EqualFold(c, col) {
			return i
		}
	}
	fail("カラムが存在しません: %s（存在するカラム: %s）", col, strings.Join(t.Columns, ","))
	return -1
}

func (t *Table) Has(col string) bool {
	for _, c := range t.Columns {
		if strings.EqualFold(c, col) {
			return true
		}
	}
	return false
}

var (
	dataDir string
	out     io.Writer = os.Stdout
)

func main() {
	args := os.Args[1:]
	// ダブルクリックなど、引数なしで端末から起動されたときは管理画面を開く
	if len(args) == 0 && term.IsTerminal(int(os.Stdin.Fd())) {
		args = []string{"gui"}
	}
	os.Exit(run(args))
}

func run(argv []string) (code int) {
	args := append([]string{}, argv...)
	out = os.Stdout
	defer func() {
		if r := recover(); r != nil {
			var de dbError
			if e, ok := r.(error); ok && errors.As(e, &de) {
				fmt.Fprintln(os.Stderr, "エラー:", de.msg)
				code = 1
				return
			}
			if e, ok := r.(error); ok {
				fmt.Fprintln(os.Stderr, "ファイルエラー:", e)
				code = 2
				return
			}
			panic(r)
		}
	}()

	enc := takeOption(&args, "--encoding")
	switch strings.ToLower(enc) {
	case "", "utf8", "utf-8":
	case "sjis", "shift_jis", "cp932":
		w := transform.NewWriter(os.Stdout, japanese.ShiftJIS.NewEncoder())
		defer w.Close()
		out = w
	default:
		fail("不明な文字コードです: %s（utf8 / sjis）", enc)
	}

	dataDir = takeOption(&args, "--db")
	if dataDir == "" {
		dataDir = os.Getenv("MINIDB_DIR")
	}
	if dataDir == "" {
		dataDir = filepath.Join(exeDir(), "data")
	}

	if len(args) == 0 {
		usage(os.Stderr) // 標準出力は他システムが読むので汚さない
		return 1
	}
	cmd := strings.ToLower(args[0])
	args = args[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage(out)
	case "version", "--version":
		fmt.Fprintln(out, "minidb", version)
	case "gui":
		gui(args)
	case "select":
		selectRows(args)
	case "tables":
		for _, n := range tableNames() {
			fmt.Fprintln(out, n)
		}
	case "columns":
		for _, c := range load(need(args, 0, "テーブル名")).Columns {
			fmt.Fprintln(out, c)
		}
	default:
		fail("不明なコマンドです: %s（help で使い方を表示。データの編集は minidb gui で行えます）", cmd)
	}
	return 0
}

// ---------- 出力 ----------

func selectRows(args []string) {
	format := strings.ToLower(takeOption(&args, "--format"))
	if format == "" {
		format = "csv"
	}
	noHeader := takeFlag(&args, "--no-header")
	limitStr := takeOption(&args, "--limit")
	wheres := takeOptions(&args, "--where")
	name := need(args, 0, "テーブル名")
	t := load(name)

	cols := t.Columns
	if len(args) > 1 && args[1] != "*" {
		cols = splitCols(args[1])
	}
	idx := make([]int, len(cols))
	outCols := make([]string, len(cols))
	for i, c := range cols {
		idx[i] = t.IndexOf(c)
		outCols[i] = t.Columns[idx[i]]
	}

	rows := filter(t, wheres)
	if limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil || n < 0 {
			fail("--limit には0以上の整数を指定してください: %s", limitStr)
		}
		if n < len(rows) {
			rows = rows[:n]
		}
	}
	result := make([][]string, len(rows))
	for i, r := range rows {
		result[i] = make([]string, len(idx))
		for j, k := range idx {
			result[i][j] = t.Rows[r][k]
		}
	}

	switch format {
	case "csv", "tsv":
		w := csv.NewWriter(out)
		if format == "tsv" {
			w.Comma = '\t'
		}
		if !noHeader {
			check(w.Write(outCols))
		}
		check(w.WriteAll(result))
	case "json":
		var b bytes.Buffer
		b.WriteByte('[')
		for i, r := range result {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('{')
			for j, c := range outCols {
				if j > 0 {
					b.WriteByte(',')
				}
				b.Write(jsonString(c))
				b.WriteByte(':')
				b.Write(jsonString(r[j]))
			}
			b.WriteByte('}')
		}
		b.WriteString("]\n")
		_, err := out.Write(b.Bytes())
		check(err)
	case "value":
		// 値だけを1行ずつタブ区切りで出す（シェルの変数に入れる用途など）
		for _, r := range result {
			fmt.Fprintln(out, strings.Join(r, "\t"))
		}
	default:
		fail("不明な形式です: %s（csv / tsv / json / value）", format)
	}
}

// ---------- 編集（GUIから呼ばれる） ----------

func createTable(name string, cols []string) {
	cols = trimAll(cols)
	checkColumns(cols)
	if exists(pathOf(name)) {
		fail("テーブルは既に存在します: %s", name)
	}
	save(name, &Table{Columns: cols})
}

// テーブル全体を置き換える（行・カラムの追加/変更/削除はGUI側で行い、ここでまとめて保存）
func replaceTable(name string, t *Table) {
	if !exists(pathOf(name)) {
		fail("テーブルが存在しません: %s", name)
	}
	t.Columns = trimAll(t.Columns)
	checkColumns(t.Columns)
	for i, r := range t.Rows {
		row := make([]string, len(t.Columns))
		copy(row, r)
		t.Rows[i] = row
	}
	save(name, t)
}

func dropTable(name string) {
	p := pathOf(name)
	if !exists(p) {
		fail("テーブルが存在しません: %s", name)
	}
	check(os.Remove(p))
}

// CSV/TSV を取り込む。1行目はカラム名。テーブルやカラムが無ければ作る。mode: append（追記）/ replace（置き換え）
func importData(name string, raw []byte, filename, mode string) int {
	if mode != "append" && mode != "replace" {
		fail("取り込み方法は append か replace を指定してください")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	sep := ','
	if ext == ".tsv" || ext == ".txt" {
		sep = '\t'
	}
	data := parseCSV(decodeText(raw), sep)
	if len(data) == 0 {
		fail("ファイルが空です")
	}
	header := trimAll(data[0])
	checkColumns(header)

	t := &Table{}
	if exists(pathOf(name)) && mode == "append" {
		t = load(name)
	}
	for _, h := range header {
		if !t.Has(h) {
			t.Columns = append(t.Columns, h)
			for i := range t.Rows {
				t.Rows[i] = append(t.Rows[i], "")
			}
		}
	}
	idx := make([]int, len(header))
	for i, h := range header {
		idx[i] = t.IndexOf(h)
	}
	for _, src := range data[1:] {
		row := make([]string, len(t.Columns))
		for i := 0; i < len(idx) && i < len(src); i++ {
			row[idx[i]] = src[i]
		}
		t.Rows = append(t.Rows, row)
	}
	save(name, t)
	return len(data) - 1
}

// ---------- 条件 ----------

// --where は "カラム=値" "カラム!=値" "カラム>値" "カラム<値" "カラム>=値" "カラム<=値" "カラム~部分一致"。複数指定は AND。
var condRe = regexp.MustCompile(`^(.+?)(!=|>=|<=|=|>|<|~)(.*)$`)

func filter(t *Table, wheres []string) []int {
	type cond struct {
		idx       int
		op, value string
	}
	conds := make([]cond, len(wheres))
	for i, w := range wheres {
		m := condRe.FindStringSubmatch(w)
		if m == nil {
			fail("条件の形式が不正です: %s", w)
		}
		conds[i] = cond{t.IndexOf(strings.TrimSpace(m[1])), m[2], m[3]}
	}
	var hits []int
	for r, row := range t.Rows {
		ok := true
		for _, c := range conds {
			if !match(row[c.idx], c.op, c.value) {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, r)
		}
	}
	return hits
}

func match(actual, op, expected string) bool {
	switch op {
	case "=":
		return actual == expected
	case "!=":
		return actual != expected
	case "~":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(expected))
	}
	var cmp int
	a, errA := strconv.ParseFloat(strings.TrimSpace(actual), 64)
	b, errB := strconv.ParseFloat(strings.TrimSpace(expected), 64)
	if errA == nil && errB == nil {
		switch {
		case a < b:
			cmp = -1
		case a > b:
			cmp = 1
		}
	} else {
		cmp = strings.Compare(actual, expected)
	}
	switch op {
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	case ">=":
		return cmp >= 0
	default:
		return cmp <= 0
	}
}

// ---------- 保存 ----------

var nameRe = regexp.MustCompile(`^[\p{L}\p{N}_\-]+$`)

func pathOf(name string) string {
	if !nameRe.MatchString(name) {
		fail("テーブル名には文字・数字・_・- のみ使えます: %s", name)
	}
	return filepath.Join(dataDir, name+".csv")
}

func tableNames() []string {
	files, err := filepath.Glob(filepath.Join(dataDir, "*.csv"))
	check(err)
	names := []string{}
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ".csv"))
	}
	sort.Strings(names)
	return names
}

func load(name string) *Table {
	p := pathOf(name)
	if !exists(p) {
		fail("テーブルが存在しません: %s", name)
	}
	raw, err := os.ReadFile(p)
	check(err)
	data := parseCSV(decodeText(raw), ',')
	t := &Table{Columns: []string{}, Rows: [][]string{}}
	if len(data) == 0 {
		return t
	}
	t.Columns = data[0]
	for _, r := range data[1:] {
		row := make([]string, len(t.Columns))
		copy(row, r)
		t.Rows = append(t.Rows, row)
	}
	return t
}

func save(name string, t *Table) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	check(w.Write(t.Columns))
	check(w.WriteAll(t.Rows))
	check(os.MkdirAll(dataDir, 0o755))
	p := pathOf(name)
	tmp := p + ".tmp"
	check(os.WriteFile(tmp, b.Bytes(), 0o644))
	check(os.Rename(tmp, p)) // 途中で落ちても元ファイルが壊れないよう一時ファイル経由
}

func parseCSV(text string, sep rune) [][]string {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = sep
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		fail("CSVの読み込みに失敗しました: %v", err)
	}
	return rows
}

// BOM付きUTF-8 / UTF-8 / Shift_JIS（Excelで保存したCSV）を自動判別する
func decodeText(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(b) {
		return string(b)
	}
	s, _, err := transform.Bytes(japanese.ShiftJIS.NewDecoder(), b)
	if err != nil {
		fail("文字コードを判別できません（UTF-8 か Shift_JIS で保存してください）")
	}
	return string(s)
}

// ---------- ユーティリティ ----------

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Dir(p)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func info(format string, a ...interface{}) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

func need(args []string, i int, what string) string {
	if len(args) <= i {
		fail("%s を指定してください", what)
	}
	return args[i]
}

func takeOptions(args *[]string, opt string) []string {
	var values, rest []string
	a := *args
	for i := 0; i < len(a); i++ {
		if strings.EqualFold(a[i], opt) {
			if i+1 >= len(a) {
				fail("%s の値がありません", opt)
			}
			values = append(values, a[i+1])
			i++
			continue
		}
		rest = append(rest, a[i])
	}
	*args = rest
	return values
}

func takeOption(args *[]string, opt string) string {
	v := takeOptions(args, opt)
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func takeFlag(args *[]string, flag string) bool {
	found := false
	var rest []string
	for _, a := range *args {
		if strings.EqualFold(a, flag) {
			found = true
		} else {
			rest = append(rest, a)
		}
	}
	*args = rest
	return found
}

func splitCols(s string) []string {
	var cols []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

func trimAll(s []string) []string {
	r := make([]string, len(s))
	for i, v := range s {
		r[i] = strings.TrimSpace(v)
	}
	return r
}

func checkColumns(cols []string) {
	if len(cols) == 0 {
		fail("カラムを1つ以上指定してください")
	}
	seen := map[string]bool{}
	for _, c := range cols {
		if c == "" {
			fail("空のカラム名があります")
		}
		k := strings.ToLower(c)
		if seen[k] {
			fail("カラム名が重複しています: %s", c)
		}
		seen[k] = true
	}
}

func jsonString(s string) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	check(e.Encode(s))
	return bytes.TrimRight(b.Bytes(), "\n")
}

func usage(w io.Writer) {
	fmt.Fprint(w, `MiniDb `+version+` - 簡易データベース

使い方: minidb <コマンド> [引数] [オプション]

  gui                                             管理画面をブラウザで開く（テーブル作成・編集・CSVインポート）
                                                  ※ 引数なしでダブルクリック起動した場合もこれになる
  select  <テーブル> [カラム1,カラム2 | *] [--where 条件]... [--format csv|tsv|json|value] [--no-header] [--limit N]
                                                  該当データを標準出力へ
  tables                                          テーブル一覧を標準出力へ
  columns <テーブル>                              カラム一覧を標準出力へ
  version                                         バージョン表示

条件(--where): カラム=値  カラム!=値  カラム>値  カラム<値  カラム>=値  カラム<=値  カラム~部分一致
               複数指定は AND。数値同士なら数値として比較。

共通オプション:
  --db <フォルダ>        データ保存先（既定: 実行ファイルと同じ場所の data フォルダ。環境変数 MINIDB_DIR でも可）
  --encoding utf8|sjis   標準出力の文字コード（既定: utf8）
gui のオプション:
  --port <番号>          管理画面のポート（既定: 空いているポートを自動で使う）
  --no-browser           ブラウザを自動で開かない

終了コード: 0=成功 1=入力エラー 2=ファイルエラー（メッセージは標準エラー出力）
`)
}
