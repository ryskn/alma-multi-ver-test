# alma-multi-ver-test API Reference

## Overview

alma-multi-ver-test は AlmaLinux 8/9/10 の VM 上で同時にシェルスクリプトを実行し、RPM ビルド・テストなどを行う IaC テストツール。ホスト（controller）から各 VM 内の agent に gRPC で通信する。

## Architecture

```
┌──────────────┐      gRPC (port 50051)     ┌──────────────┐
│  alma-ctl    │──────────────────────────── │  alma-agent  │ (alma8)
│  (controller)│──────────────────────────── │  alma-agent  │ (alma9)
│              │──────────────────────────── │  alma-agent  │ (alma10)
└──────────────┘                            └──────────────┘
```

- **alma-agent**: 各 VM 内で動作する gRPC サーバ（デフォルト `:50051`）
- **alma-ctl**: ホスト側 CLI。YAML ジョブ定義を読み込み、各 agent に RPC を発行

## Quick Start

### ビルド

```bash
make build          # agent + controller をビルド
make proto          # .proto → .pb.go 再生成
make agent          # agent のみ（CGO_ENABLED=0 でスタティックビルド）
make controller     # controller のみ
```

### VM 起動・停止

```bash
make up             # vagrant up（agent ビルド後、全 VM 起動）
make down           # vagrant halt
```

### コマンド

```bash
# ヘルスチェック
alma-ctl ping

# スクリプト実行（全 VM で同時実行）
alma-ctl exec scripts/example.sh

# YAML ジョブ実行
alma-ctl [-v|-vv|-vvv] run jobs/example.yaml
```

### Verbosity

| Flag   | 動作                                      |
|--------|-------------------------------------------|
| (なし) | ステップの成功/失敗のみ                    |
| `-v`   | 各ターゲットの stdout 末尾 3 行を表示      |
| `-vv`  | stdout をリアルタイムストリーミング表示     |
| `-vvv` | stdout + stderr をリアルタイムストリーミング表示 |

---

## gRPC API

### Service: `Agent`

```protobuf
service Agent {
  rpc ExecScript(ExecRequest)  returns (ExecResponse);
  rpc ExecStream(ExecRequest)  returns (stream ExecOutput);
  rpc Upload(stream UploadChunk) returns (UploadResponse);
  rpc Ping(PingRequest)        returns (PingResponse);
}
```

---

### `Ping`

ヘルスチェック。agent のホスト名と OS 情報を返す。

**Request**: `PingRequest` (empty)

**Response**: `PingResponse`

| Field      | Type   | Description                          |
|------------|--------|--------------------------------------|
| `hostname` | string | VM のホスト名                         |
| `os_info`  | string | `/etc/os-release` の PRETTY_NAME     |

**例**:
```bash
# grpcurl で直接呼び出し
grpcurl -plaintext 192.168.200.10:50051 alma.Agent/Ping
```

---

### `ExecScript`

シェルスクリプトを VM 上で実行し、完了後に stdout/stderr/exit code を一括で返す。

**Request**: `ExecRequest`

| Field         | Type   | Description                    |
|---------------|--------|--------------------------------|
| `script_name` | string | スクリプト名（ログ識別用）       |
| `script_body` | string | 実行するシェルスクリプトの本文    |

**Response**: `ExecResponse`

| Field       | Type   | Description        |
|-------------|--------|--------------------|
| `exit_code` | int32  | プロセスの終了コード |
| `stdout`    | string | 標準出力全体         |
| `stderr`    | string | 標準エラー出力全体   |

**動作**:
1. `script_body` を一時ファイル (`/tmp/alma-*.sh`) に書き込む
2. `/bin/bash` で実行（`HOME=/root`, `PATH` をセット）
3. 完了後、stdout/stderr を一括返却
4. タイムアウト: コンテキストに依存（controller のデフォルトは 5 分）

---

### `ExecStream`

シェルスクリプトを VM 上で実行し、stdout/stderr を **行単位でリアルタイムにストリーミング** する。

**Request**: `ExecRequest`（ExecScript と同じ）

**Response**: `stream ExecOutput`

| Field       | Type   | Description                              |
|-------------|--------|------------------------------------------|
| `line`      | string | 出力の 1 行                               |
| `is_stderr` | bool   | `true` なら stderr の行                   |
| `done`      | bool   | `true` なら最終メッセージ（出力終了）      |
| `exit_code` | int32  | `done=true` のときのみ有効、終了コード     |

**動作**:
1. `ExecScript` と同じくスクリプトを一時ファイルに書き込み
2. `cmd.StdoutPipe()` / `cmd.StderrPipe()` で stdout/stderr をパイプ取得
3. 2 つの goroutine が `bufio.Scanner` で行を読み、即座に `stream.Send()` で返す
4. バッファサイズ: 256KB/行
5. 全行送信後、`done=true` と `exit_code` を含む最終メッセージを送信

**使用例（Go クライアント）**:
```go
stream, err := client.ExecStream(ctx, &pb.ExecRequest{
    ScriptName: "example",
    ScriptBody: "echo hello; sleep 1; echo world",
})
for {
    out, err := stream.Recv()
    if err != nil {
        break
    }
    if out.Done {
        fmt.Printf("exit code: %d\n", out.ExitCode)
        break
    }
    fmt.Println(out.Line)
}
```

---

### `Upload`

ファイルまたは tar アーカイブを VM にアップロードする。クライアントストリーミング RPC。

**Request**: `stream UploadChunk`

| Field       | Type   | Description                                    |
|-------------|--------|-------------------------------------------------|
| `dest_path` | string | VM 上の送信先パス（最初のチャンクでのみ設定）      |
| `data`      | bytes  | ファイル/tar のデータチャンク                     |
| `is_tar`    | bool   | `true` なら `dest_path` に tar として展開する     |

**Response**: `UploadResponse`

| Field           | Type  | Description        |
|-----------------|-------|--------------------|
| `bytes_written` | int64 | 書き込んだ合計バイト数 |

**動作**:
- チャンクサイズ: 64KB（controller 側のデフォルト）
- `is_tar=true` の場合: gzip 圧縮の自動検出あり → `dest_path` に展開
- `is_tar=false` の場合: `dest_path` にファイルとして書き込み
- シンボリックリンク対応、パストラバーサル防止付き

---

## YAML ジョブ定義

### 構造

```yaml
name: job-name                # ジョブ名（ログディレクトリ名に使用）

targets:                      # 省略時は全ターゲット (alma8, alma9, alma10)
  - alma8
  - alma9

vars:                         # ターゲットごとの変数（{{key}} で展開）
  alma8:
    branch: el8
  alma9:
    branch: el9

steps:
  - name: step name           # ステップ名
    run: |                    # シェルスクリプト（{{var}} は vars で展開）
      echo "Hello from {{target}}"

  - name: upload step
    upload:                   # ファイル/ディレクトリのアップロード
      src: ./local/path       # ローカルパス
      dest: /remote/path      # VM 上のパス
      git_archive: "{{branch}}"  # (optional) git archive でブランチを tar 化

  - name: combined step       # upload + run を同一ステップで指定可
    upload:
      src: ../my-project/
      dest: /tmp/src/
      git_archive: main
    run: |
      cd /tmp/src && make
```

### 組み込み変数

| 変数         | 値                                  |
|-------------|--------------------------------------|
| `{{target}}`| ターゲット名（`alma8`, `alma9` 等）    |

### ログ

ジョブ実行時、`logs/<job-name>/<timestamp>/` 以下にターゲットごとのログが生成される。

```
logs/
  build-vagrant-libvirt/
    20260311-143000/
      alma8.log
      alma9.log
      alma10.log
```

---

## Agent サーバのセットアップ

### 前提条件

- AlmaLinux 8/9/10（他の RHEL 系でも動作可能）
- ネットワーク疎通（controller → agent の TCP 50051 ポート）

### ビルド

```bash
# スタティックバイナリ（VM に scp で配置可能）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o alma-agent ./agent
```

### 手動デプロイ

```bash
# VM にバイナリをコピー
scp alma-agent root@<vm-ip>:/usr/local/bin/

# VM 上で起動
ssh root@<vm-ip> '/usr/local/bin/alma-agent &'

# カスタムポートで起動
ssh root@<vm-ip> '/usr/local/bin/alma-agent -listen :9090 &'
```

### systemd サービスとして登録

```ini
# /etc/systemd/system/alma-agent.service
[Unit]
Description=alma-agent gRPC server
After=network.target

[Service]
ExecStart=/usr/local/bin/alma-agent -listen :50051
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now alma-agent
```

### Vagrant 環境（自動セットアップ）

`vagrant up` で自動的に agent がデプロイ・起動される。ターゲット一覧:

| ターゲット | IP               | ポート |
|-----------|------------------|--------|
| alma8     | 192.168.200.10   | 50051  |
| alma9     | 192.168.200.11   | 50051  |
| alma10    | 192.168.200.12   | 50051  |

### リモート（非 Vagrant）環境での利用

Vagrant 以外のサーバで使う場合:

1. **agent のデプロイ**: 上記のビルド・scp・systemd 手順に従う

2. **controller のターゲット設定**: `controller/main.go` の `targets` を編集

   ```go
   var targets = []struct {
       Name string
       Addr string
   }{
       {"server1", "10.0.0.1:50051"},
       {"server2", "10.0.0.2:50051"},
       {"server3", "10.0.0.3:50051"},
   }
   ```

3. **ファイアウォール**: TCP 50051 を開放

   ```bash
   firewall-cmd --add-port=50051/tcp --permanent
   firewall-cmd --reload
   ```

4. **controller をリビルド**:
   ```bash
   make controller
   ```

### セキュリティ注意事項

- 現在の agent は **認証なし**（`insecure.NewCredentials()`）で動作する
- agent はスクリプトを **root 権限** で実行する
- プロダクション利用時は以下を検討:
  - TLS 証明書の導入（gRPC の `credentials.NewTLS()`）
  - mTLS（相互認証）
  - agent の実行ユーザ制限
  - ネットワークセグメンテーション

---

## protobuf / gRPC コード生成

proto ファイルの変更後:

```bash
make proto
```

必要なツール:
```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```
