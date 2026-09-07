// srcwatch — 一次ソース更新チェッカー
// 登録したURL（財務省・日銀・SEC・MAS・金融庁など）の本文を取得し、
// 前回との差分を検知して「いつ・どこが・どう変わったか」を出力する。
// 標準ライブラリのみ。外部依存なし。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Source は監視対象1件。
type Source struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Note   string `json:"note,omitempty"`   // 何を見る場所か
	Follow string `json:"follow,omitempty"` // 正規表現。マッチ行だけを監視対象にする（任意）
}

// Config は sources.json の形。
type Config struct {
	UserAgent string   `json:"user_agent"`
	TimeoutS  int      `json:"timeout_seconds"`
	Sources   []Source `json:"sources"`
}

// Snapshot は1件の前回状態。
type Snapshot struct {
	Hash      string   `json:"hash"`
	CheckedAt string   `json:"checked_at"`
	ChangedAt string   `json:"changed_at"`
	Lines     []string `json:"lines"`
	Status    int      `json:"status"`
	Err       string   `json:"err,omitempty"`
}

// State は URL をキーにした前回状態の集合。
type State map[string]Snapshot

// ReportItem は Web 表示用の1件。
type ReportItem struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Note       string   `json:"note,omitempty"`
	Result     string   `json:"result"` // changed / same / first / failed
	Status     int      `json:"status"`
	Err        string   `json:"err,omitempty"`
	CheckedAt  string   `json:"checked_at"`
	ChangedAt  string   `json:"changed_at"`
	LineCount  int      `json:"line_count"`
	Added      []string `json:"added,omitempty"`
	Removed    []string `json:"removed,omitempty"`
	AddedMore  int      `json:"added_more,omitempty"`
	RemovedMor int      `json:"removed_more,omitempty"`
}

// HistoryEvent は変化があった時の記録（Web の履歴欄）。
type HistoryEvent struct {
	At      string   `json:"at"`
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// Report は -out で書き出す JSON。
type Report struct {
	GeneratedAt string         `json:"generated_at"`
	Summary     map[string]int `json:"summary"`
	Items       []ReportItem   `json:"items"`
	History     []HistoryEvent `json:"history"`
}

var (
	reTag    = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</\s*(script|style|noscript)\s*>`)
	reBlock  = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|td|th|h[1-6]|section|article|table|ul|ol|dd|dt)[^>]*>`)
	reAnyTag = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace  = regexp.MustCompile(`[ \t\x{3000}]+`)
)

const maxHistory = 200

func main() {
	cfgPath := flag.String("config", "sources.json", "監視対象の一覧（JSON）")
	statePath := flag.String("state", "state.json", "前回の状態を保存するファイル")
	outPath := flag.String("out", "", "Web表示用のレポートJSONを書き出す先（任意）")
	showDiff := flag.Bool("diff", true, "変化した行を表示する")
	maxLines := flag.Int("max", 40, "差分表示の最大行数")
	only := flag.String("only", "", "名前に含まれる文字で対象を絞る")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	state := loadState(*statePath)
	history := loadHistory(*outPath)
	timeout := cfg.TimeoutS
	if timeout < 10 {
		timeout = 10
	}
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	jst := time.FixedZone("JST", 9*3600)
	now := time.Now().In(jst).Format("2006-01-02 15:04:05")

	rep := Report{GeneratedAt: now, Summary: map[string]int{}}
	for _, s := range cfg.Sources {
		if *only != "" && !strings.Contains(s.Name, *only) {
			continue
		}
		item := ReportItem{Name: s.Name, URL: s.URL, Note: s.Note, CheckedAt: now}
		lines, status, ferr := fetch(client, cfg.UserAgent, s)
		prev, had := state[s.URL]
		snap := Snapshot{CheckedAt: now, Status: status, ChangedAt: prev.ChangedAt}
		item.Status, item.ChangedAt = status, prev.ChangedAt
		if ferr != nil {
			snap.Err = ferr.Error()
			snap.Hash, snap.Lines = prev.Hash, prev.Lines
			state[s.URL] = snap
			item.Result, item.Err, item.LineCount = "failed", ferr.Error(), len(prev.Lines)
			rep.Summary["failed"]++
			rep.Items = append(rep.Items, item)
			fmt.Printf("[取得失敗] %s  %s\n           %v\n", s.Name, s.URL, ferr)
			continue
		}
		h := hashLines(lines)
		snap.Hash, snap.Lines = h, lines
		item.LineCount = len(lines)
		switch {
		case !had:
			snap.ChangedAt = now
			item.Result, item.ChangedAt = "first", now
			rep.Summary["first"]++
			fmt.Printf("[初回登録] %s  (%d行)  %s\n", s.Name, len(lines), s.URL)
		case prev.Hash == h:
			item.Result = "same"
			rep.Summary["same"]++
			fmt.Printf("[変化なし] %s  (前回の変化 %s)\n", s.Name, orDash(prev.ChangedAt))
		default:
			snap.ChangedAt = now
			item.Result, item.ChangedAt = "changed", now
			rep.Summary["changed"]++
			added, removed := setDiff(prev.Lines, lines)
			item.Added, item.AddedMore = clip(added, *maxLines)
			item.Removed, item.RemovedMor = clip(removed, *maxLines)
			history = append([]HistoryEvent{{At: now, Name: s.Name, URL: s.URL, Added: item.Added, Removed: item.Removed}}, history...)
			fmt.Printf("[★変化あり] %s  %s\n            前回の変化 %s → 今回 %s\n", s.Name, s.URL, orDash(prev.ChangedAt), now)
			if *showDiff {
				printDiff(added, removed, *maxLines)
			}
		}
		state[s.URL] = snap
		rep.Items = append(rep.Items, item)
	}
	if len(history) > maxHistory {
		history = history[:maxHistory]
	}
	rep.History = history
	if err := saveState(*statePath, state); err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
	}
	if *outPath != "" {
		if err := writeJSON(*outPath, rep); err != nil {
			fmt.Fprintln(os.Stderr, "out:", err)
		}
	}
	fmt.Printf("\n%s  変化 %d / 変化なし %d / 初回 %d / 失敗 %d\n", now, rep.Summary["changed"], rep.Summary["same"], rep.Summary["first"], rep.Summary["failed"])
	if rep.Summary["changed"] > 0 {
		os.Exit(1) // 変化ありは終了コード1（スクリプトや通知から拾える）
	}
}

func loadConfig(p string) (*Config, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.UserAgent == "" {
		c.UserAgent = "srcwatch/1.0 (+https://github.com/anpon10/srcwatch)"
	}
	return &c, nil
}

func loadState(p string) State {
	st := State{}
	b, err := os.ReadFile(p)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// loadHistory は前回の -out JSON から履歴だけを引き継ぐ。
func loadHistory(p string) []HistoryEvent {
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var r Report
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return r.History
}

func saveState(p string, st State) error { return writeJSON(p, st) }

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// fetch はページ本文を取り、タグを外し、行に正規化して返す。
func fetch(c *http.Client, ua string, s Source) ([]string, int, error) {
	req, err := http.NewRequest("GET", s.URL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "ja,en;q=0.8")
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	lines := normalize(string(body))
	if s.Follow != "" {
		re, err := regexp.Compile(s.Follow)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("follow regexp: %w", err)
		}
		var kept []string
		for _, l := range lines {
			if re.MatchString(l) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	return lines, resp.StatusCode, nil
}

// normalize はHTMLを「意味のある行」の列にする。
func normalize(s string) []string {
	s = reTag.ReplaceAllString(s, " ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reAnyTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(reSpace.ReplaceAllString(l, " "))
		if len(l) < 2 {
			continue
		}
		out = append(out, l)
	}
	return out
}

func hashLines(lines []string) string {
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// setDiff は集合差分（追加行 / 削除行）。順序の入れ替えは無視する。
func setDiff(prev, cur []string) (added, removed []string) {
	pset := map[string]bool{}
	for _, l := range prev {
		pset[l] = true
	}
	cset := map[string]bool{}
	for _, l := range cur {
		cset[l] = true
	}
	for _, l := range cur {
		if !pset[l] {
			added = append(added, l)
		}
	}
	for _, l := range prev {
		if !cset[l] {
			removed = append(removed, l)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}

// clip は先頭 n 行だけ残し、残りの行数を返す。長い行は160字で切る。
func clip(xs []string, n int) ([]string, int) {
	out := make([]string, 0, min(len(xs), n))
	for i, l := range xs {
		if i >= n {
			return out, len(xs) - n
		}
		if len(l) > 160 {
			l = l[:160] + "…"
		}
		out = append(out, l)
	}
	return out, 0
}

func printDiff(added, removed []string, maxN int) {
	show := func(mark string, xs []string) {
		c, more := clip(xs, maxN)
		for _, l := range c {
			fmt.Printf("            %s %s\n", mark, l)
		}
		if more > 0 {
			fmt.Printf("            %s …ほか%d行\n", mark, more)
		}
	}
	show("+", added)
	show("-", removed)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
