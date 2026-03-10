# alma - AlmaLinux マルチバージョン IaC テスト環境

AlmaLinux 8/9/10 の Vagrant VM を起動し、gRPC 経由でシェルスクリプトを一斉実行・結果比較するツール。
ホストのデータプレーンは VPP。

## アーキテクチャ

```
┌─ Host (Fedora 43) ──────────────────────────────────┐
│                                                       │
│  VPP v26 (データプレーン)                             │
│                                                       │
│  alma-ctl (Go/gRPC) ──→ alma-agent@各VM:50051        │
│                                                       │
│  ┌──────┐  ┌──────┐  ┌──────┐  Vagrant/libvirt       │
│  │alma8 │  │alma9 │  │alma10│                         │
│  │Agent │  │Agent │  │Agent │                         │
│  └──────┘  └──────┘  └──────┘                         │
│  192.168    192.168    192.168                         │
│  .200.10    .200.11    .200.12                         │
└───────────────────────────────────────────────────────┘
```

## 前提条件

- Fedora 43 ホスト
- libvirt + vagrant + vagrant-libvirt
- Go 1.25+, protoc, protoc-gen-go, protoc-gen-go-grpc
- VPP (nucleo COPR または ソースビルド)
- SELinux は `permissive` にしないと VPP が起動しない (`sudo setenforce 0`)

## libvirt ネットワーク準備 (初回のみ)

```bash
# alma-net を作成
sudo virsh net-define /dev/stdin <<'EOF'
<network>
  <name>alma-net</name>
  <forward mode="nat"/>
  <bridge name="virbr10" stp="on" delay="0"/>
  <ip address="192.168.200.1" netmask="255.255.255.0"/>
</network>
EOF
sudo virsh net-start alma-net
sudo virsh net-autostart alma-net
```

## クイックスタート

```bash
# 1. ビルド (agent + controller)
make build

# 2. VM 起動 (AlmaLinux 8/9/10 を並行起動)
make up

# 3. 全 VM の接続確認
make ping
# => [alma8]  OK  hostname=alma8  os=AlmaLinux 8.10 (Cerulean Leopard)
# => [alma9]  OK  hostname=alma9  os=AlmaLinux 9.7 (Moss Jungle Cat)
# => [alma10] OK  hostname=alma10 os=AlmaLinux 10.1 (Heliotrope Lion)

# 4. スクリプトを全バージョンで実行
make exec S=scripts/example.sh

# 5. VM 停止
make down

# 6. VM 削除
vagrant destroy -f
```

## Make ターゲット一覧

| コマンド | 説明 |
|---|---|
| `make build` | agent (linux/amd64 static) と controller をビルド |
| `make proto` | proto/alma.proto から Go コードを生成 |
| `make agent` | gRPC エージェントバイナリをビルド |
| `make controller` | ホスト側 CLI (`alma-ctl`) をビルド |
| `make up` | Vagrant VM 3台を起動 (agent も自動デプロイ) |
| `make down` | VM を停止 (データは保持) |
| `make ping` | 全 VM に gRPC Ping して接続確認 |
| `make exec S=<script>` | 指定スクリプトを全 VM で実行し結果表示 |
| `make build-vl` | vagrant-libvirt を各 VM でRPMビルド (el8/el9/el10ブランチ) |
| `make clean` | ビルド成果物を削除 |

## alma-ctl コマンド

```bash
# 全 VM の接続確認
./controller/alma-ctl ping

# スクリプトを全 VM で実行
./controller/alma-ctl exec scripts/example.sh
```

## 自作スクリプトを全バージョンでテスト

`scripts/` ディレクトリにシェルスクリプトを置いて実行:

```bash
cat > scripts/check-openssl.sh << 'EOF'
#!/bin/bash
openssl version
rpm -q openssl
EOF

make exec S=scripts/check-openssl.sh
```

出力例:
```
=== alma8 === (exit 0)
[stdout]
OpenSSL 1.1.1k  FIPS 25 Mar 2021
openssl-1.1.1k-14.el8_10.x86_64

=== alma9 === (exit 0)
[stdout]
OpenSSL 3.0.7 1 Nov 2022 (Library: OpenSSL 3.0.7 1 Nov 2022)
openssl-3.0.7-28.el9_4.x86_64

=== alma10 === (exit 0)
[stdout]
OpenSSL 3.2.2 4 Jun 2024
openssl-3.2.2-8.el10.x86_64
```

## vagrant-libvirt RPM ビルド

`../vagrant-libvirt/` リポジトリの el8/el9/el10 ブランチから各 VM で RPM をビルド:

```bash
make build-vl
```

ビルド結果は各 VM の `/root/rpmbuild/RPMS/noarch/` に保存される。

手動で個別 VM をビルドする場合:

```bash
# ソースを転送
(cd ../vagrant-libvirt && git archive --prefix=vagrant-libvirt-src/ el8) | \
  vagrant ssh alma8 -c "sudo rm -rf /tmp/vagrant-libvirt-src && sudo tar xf - -C /tmp"

# ビルド実行
cat scripts/build-vagrant-libvirt.sh | \
  vagrant ssh alma8 -c "cat > /tmp/build-vl.sh && sudo bash /tmp/build-vl.sh"
```

## ファイル構成

```
alma/
├── Vagrantfile                         # VM 定義 (AlmaLinux 8/9/10)
├── Makefile                            # ビルド・操作コマンド
├── go.mod / go.sum                     # Go 依存関係
├── proto/
│   ├── alma.proto                      # gRPC サービス定義
│   ├── alma.pb.go                      # 生成コード
│   └── alma_grpc.pb.go                 # 生成コード
├── agent/
│   ├── main.go                         # VM 内 gRPC エージェント
│   └── alma-agent                      # ビルド済みバイナリ
├── controller/
│   ├── main.go                         # ホスト側 CLI
│   └── alma-ctl                        # ビルド済みバイナリ
└── scripts/
    ├── example.sh                      # サンプルテストスクリプト
    └── build-vagrant-libvirt.sh        # vagrant-libvirt RPM ビルドスクリプト
```

## gRPC API

| RPC | 説明 |
|---|---|
| `Ping` | VM のホスト名・OS情報を返す |
| `ExecScript` | シェルスクリプトを送信→VM内で実行→stdout/stderr/exit_code を返却 |

proto 定義: `proto/alma.proto`

## 既知の注意事項

- VPP は `sudo setenforce 0` しないと起動しない (SELinux の stat segment mmap 制限)
- alma10 の eth1 に IP が自動付与されない場合がある → `vagrant ssh alma10 -c "sudo ip addr add 192.168.200.12/24 dev eth1"`
- `make build-vl` 実行前に各 VM に `rpm-build ruby rubygems ruby-devel` が必要
