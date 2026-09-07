# srcwatch — 一次ソース更新チェッカー

財務省・日銀・金融庁・SEC・BIS・米財務省・MASなど、**一次ソースのページが変わったか**を検知する小さなCLI。Go標準ライブラリのみ、外部依存なし。毎朝自動で走らせ、結果を1枚のWebページで見られる。

## 何をするか
1. `sources.json` のURLを順に取得
2. HTMLからタグを外し、行単位に正規化してハッシュ
3. 前回（`state.json`）と比べて **変化あり／なし／取得失敗** を表示。変化した行は `+`（追加）`-`（削除）で出す
4. 変化が1件でもあれば終了コード `1`（スクリプトや通知から拾える）
5. `-out data.json` を付けると、Web表示用のレポート（各件の状態＋変化の履歴）を書き出す

## 使い方（CLI）
```
go build -o srcwatch.exe .
./srcwatch.exe                                        # sources.json / state.json を既定で使う
./srcwatch.exe -only 財務省                           # 名前で絞る
./srcwatch.exe -state docs/state.json -out docs/data.json   # Web用に書き出す
```
初回は全件「初回登録」になる。2回目以降が本番。

## Web版（サーバー不要）
- `docs/index.html` が `docs/data.json` を読んで表示する静的ページ
- `.github/workflows/watch.yml` が **毎日 07:00 JST** に GitHub Actions で実行し、`docs/state.json` と `docs/data.json` を更新してコミットする
- GitHub Pages を `docs/` フォルダで公開すれば、`https://<user>.github.io/srcwatch/` が「今日変わった一次ソース」の一覧になる
- 費用ゼロ。手元のPCを動かし続ける必要もない

## sources.json の書き方
```json
{"name": "表示名", "url": "https://…", "note": "何を見る場所か", "follow": "監視したい行の正規表現（任意）"}
```
`follow` を付けると、その正規表現にマッチする行だけを監視対象にする（例＝`"follow": "令和8年.*外国為替平衡操作額"`）。

## 注意
- 取得は公開ページのみ。ログインが要るページ・ボット遮断（MAS等）は取得失敗として記録される。SECはHTMLが遮断されるためRSSを見ている
- 差分は「行の集合」の差＝順序の入れ替えは無視する。数字の変化は行ごと出る
- 履歴は最新200件まで保持
