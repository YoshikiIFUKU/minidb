// コマンドからの書き込み（insert / import）と、書き込みの排他制御。
// 管理画面と他システムのコマンドが同じデータを同時に更新しても、行が消えないようにする。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

// 管理画面で開いた後に、他の操作でテーブルが更新されていた場合のエラー
type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

type importOptions struct {
	Create     bool // テーブルが無ければ作る
	AddColumns bool // 存在しないカラムがあれば追加する
	Strict     bool // 列数が見出しと合わない行をエラーにする
}

// insert <テーブル> カラム=値 ...
func insertCmd(args []string) {
	name := need(args, 0, "テーブル名")
	pairs := args[1:]
	if len(pairs) == 0 {
		fail("追加する値を カラム=値 の形式で指定してください")
	}
	withLock(func() {
		t := load(name)
		row := make([]string, len(t.Columns))
		given := map[int]bool{}
		for _, p := range pairs {
			k, v := splitPair(p)
			i := t.IndexOf(k)
			if given[i] {
				fail("カラム %s が2回指定されています", t.Columns[i])
			}
			given[i] = true
			row[i] = v
		}
		t.Rows = append(t.Rows, row)
		save(name, t)
	})
	info("テーブル %s に 1 行追加しました", name)
}

// import <テーブル> <ファイル | -> [--mode append|replace] [--sep auto|comma|tab] [--create] [--add-columns]
func importCmd(args []string) {
	mode := strings.ToLower(takeOption(&args, "--mode"))
	if mode == "" {
		mode = "append"
	}
	sep := strings.ToLower(takeOption(&args, "--sep"))
	opt := importOptions{
		Create:     takeFlag(&args, "--create"),
		AddColumns: takeFlag(&args, "--add-columns"),
		Strict:     true,
	}
	name := need(args, 0, "テーブル名")
	src := need(args, 1, "取り込むファイル（標準入力から読む場合は -）")

	var raw []byte
	var err error
	if src == "-" {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fail("標準入力にデータが渡されていません（例: type rows.csv | SampleDBApp import %s -）", name)
		}
		raw, err = io.ReadAll(os.Stdin)
	} else {
		if !exists(src) {
			fail("ファイルが見つかりません: %s", src)
		}
		raw, err = os.ReadFile(src)
	}
	check(err)
	if sep == "" {
		sep = "auto"
		if strings.EqualFold(filepath.Ext(src), ".tsv") {
			sep = "tab"
		}
	}

	var n int
	withLock(func() { n = importData(name, raw, sep, mode, opt) })
	if mode == "replace" {
		info("テーブル %s を %d 行の内容で置き換えました", name, n)
	} else {
		info("テーブル %s に %d 行追加しました", name, n)
	}
}

// データフォルダ単位で書き込みを1つずつに制限する（別プロセスのコマンドや管理画面との同時書き込み対策）。
// ロックファイルを排他作成できるまで最大10秒待つ。30秒以上残っているロックは異常終了の残骸とみなして消す。
func withLock(fn func()) {
	check(os.MkdirAll(dataDir, 0o755))
	lock := filepath.Join(dataDir, ".sampledb.lock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			break
		}
		if st, e := os.Stat(lock); e == nil && time.Since(st.ModTime()) > 30*time.Second {
			os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			fail("他の処理がデータを更新中のため書き込めませんでした（10秒待機）。しばらくしてから再実行してください（%v）", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer os.Remove(lock)
	fn()
}

// テーブルの内容から作る版数。管理画面で開いた時点から変わっていないかの確認に使う
func tableVersion(name string) string {
	raw, err := os.ReadFile(pathOf(name))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12])
}

func splitPair(s string) (string, string) {
	i := strings.Index(s, "=")
	if i <= 0 {
		fail("カラム=値 の形式で指定してください: %s", s)
	}
	return strings.TrimSpace(s[:i]), s[i+1:]
}
