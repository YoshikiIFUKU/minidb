// 管理画面（GUI）: 実行ファイルに内蔵したWebページをローカルのブラウザで開く。
// 追加のランタイムやライブラリが不要なので、どのOSでも同じように動く。
package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

//go:embed web/index.html
var indexHTML []byte

const maxUpload = 100 << 20 // 取り込みファイルの上限 100MB

func gui(args []string) {
	port := takeOption(&args, "--port")
	noBrowser := takeFlag(&args, "--no-browser")
	if port == "" {
		port = "0"
	}

	// 自分のPCからしか接続できないよう 127.0.0.1 で待ち受ける
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fail("ポート %s で待ち受けできません: %v", port, err)
	}
	// 他のWebサイトからAPIを呼ばれないよう、起動ごとにランダムな合言葉を作ってURLの#以降で画面に渡す
	b := make([]byte, 16)
	_, err = rand.Read(b)
	check(err)
	token := hex.EncodeToString(b)
	url := "http://" + ln.Addr().String() + "/#" + token

	var mu sync.Mutex
	mux := http.NewServeMux()
	handle := func(pattern string, h func(r *http.Request) interface{}) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			defer func() {
				if rec := recover(); rec != nil {
					status, msg := http.StatusInternalServerError, fmt.Sprint(rec)
					var de dbError
					if e, ok := rec.(error); ok {
						msg = e.Error()
						if errors.As(e, &de) {
							status, msg = http.StatusBadRequest, de.msg
						}
					}
					writeJSON(w, status, map[string]string{"error": msg})
				}
			}()
			writeJSON(w, http.StatusOK, h(r))
		})
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Write(indexHTML)
	})
	handle("GET /api/info", func(r *http.Request) interface{} {
		return map[string]string{"version": version, "dataDir": dataDir}
	})
	handle("GET /api/tables", func(r *http.Request) interface{} {
		return tableNames()
	})
	handle("POST /api/tables", func(r *http.Request) interface{} {
		var req struct {
			Name    string   `json:"name"`
			Columns []string `json:"columns"`
		}
		readJSON(r, &req)
		createTable(strings.TrimSpace(req.Name), req.Columns)
		return map[string]bool{"ok": true}
	})
	handle("GET /api/tables/{name}", func(r *http.Request) interface{} {
		return load(r.PathValue("name"))
	})
	handle("PUT /api/tables/{name}", func(r *http.Request) interface{} {
		var t Table
		readJSON(r, &t)
		replaceTable(r.PathValue("name"), &t)
		return map[string]bool{"ok": true}
	})
	handle("DELETE /api/tables/{name}", func(r *http.Request) interface{} {
		dropTable(r.PathValue("name"))
		return map[string]bool{"ok": true}
	})
	handle("POST /api/tables/{name}/import", func(r *http.Request) interface{} {
		raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxUpload))
		if err != nil {
			fail("ファイルを受け取れませんでした（100MBまで）: %v", err)
		}
		q := r.URL.Query()
		n := importData(r.PathValue("name"), raw, q.Get("filename"), q.Get("mode"))
		return map[string]int{"count": n}
	})

	// DNSリバインディング対策: 127.0.0.1 / localhost 以外の Host 宛ては拒否
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})

	info("MiniDb %s 管理画面を起動しました", version)
	info("  URL        : %s", url)
	info("  データ保存先: %s", dataDir)
	info("ブラウザが開かない場合は上のURLをブラウザに貼り付けてください。")
	info("この画面を閉じる（または Ctrl+C）と管理画面は終了します。")
	if !noBrowser {
		openBrowser(url)
	}
	check(http.Serve(ln, handler))
}

func readJSON(r *http.Request, v interface{}) {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxUpload)).Decode(v); err != nil {
		fail("リクエストの形式が不正です: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		info("ブラウザを自動で開けませんでした: %v", err)
	}
}
