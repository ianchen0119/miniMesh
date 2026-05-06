# 30 分鐘搞懂 Service Mesh：從 5W1H 到精簡化實作

如果你在 Kubernetes 環境工作過一段時間，應該多少聽過 Service Mesh。但多數人的理解停在「加個 sidecar 就有 mTLS 了」，然後導入之後發現維運成本暴增，又開始懷疑這東西到底值不值得。

本文將用 5W1H 的框架讓讀者清楚的了解 Service Mesh：**What**（它是什麼）、**Why**（為什麼需要）、**Who**（誰在用、適合什麼場景）、**When**（什麼時候該導入）、**Where**（運作在哪一層）、**How**（底層怎麼做到的）。
文末筆者會用一個接近千行程式碼的 Go 專案（miniMesh）帶你看原始程式碼，摸清那些看起來像魔法一樣的機制。


## 1. Service Mesh 的本質：不是「功能」，是「責任轉移」

### What：什麼是 Service Mesh

Service Mesh 是一個**基礎設施層**，負責處理服務與服務之間的所有網路通訊——加密、身份驗證、授權、流量控制、可觀測性——而且完全不需要應用程式感知它的存在。

更具體一點：你的服務 A 連線到服務 B，這條連線在抵達 B 之前，會先經過 Mesh 的資料平面（proxy）。Proxy 在這裡做 mTLS 握手、套用 policy、記錄 metrics，然後才把請求轉給 B。整個過程 A 和 B 都不知道有人在中間。

「不需要應用感知（Application Awareness）」這四個字是關鍵，也是 Mesh 和傳統 SDK-based 治理方案最大的差別。

### Why & When：為何需要 Service Mesh

Service Mesh 這個詞本身其實很容易讓人走偏。很多人一看到 Mesh，直覺反應是「加密跟流量控制」，然後把它跟 API Gateway 混在一起評估。

不如讓我們換個角度思考：**為什麼以前我們不需要 Mesh？**

當服務還少、部署還簡單的時候，每個服務自己顧好 TLS、自己做 retry、自己寫 metrics，雖然麻煩，但還撐得住。問題在規模化之後才出現——

```
A 服務用 Go TLS library，B 服務用 Java 的，C 服務是外包寫的根本沒加 TLS。
有人把 timeout 設成 30 秒，有人忘了設。
稽核的時候沒辦法回答「所有服務間通訊有沒有加密」，因為答案散在每個 repo 裡。
```

這不是技術問題，是治理問題。

Mesh 的本質是**責任轉移**——把「服務間通訊怎麼處理」這件事，從各個應用的程式碼裡抽出來，交給一個統一的資料平面負責，再讓控制面統一下發規則。

如此一來，應用不需要改程式碼，不需要知道對方有沒有在用 TLS，也不需要自己維護憑證。這件事由平台層負責。

### 責任邊界

先把邊界講清楚，後面才不會亂：

| 層級 | 負責什麼 | 不負責什麼 |
|---|---|---|
| 應用層 | 業務語意、API 定義、資料格式 | 連線安全、retry 策略、憑證 |
| 資料平面（proxy） | 封包攔截與轉發、mTLS 握手、L4/L7 policy 執行 | 服務探索邏輯、業務路由 |
| 控制面（controller） | 規則下發、憑證生命週期、服務身份管理 | 業務語意、流量本身 |

> 注意：Mesh 不是 API Gateway。API Gateway 管的是「對外暴露什麼」，Mesh 管的是「服務與服務之間怎麼通」。兩者的問題域不同，不要混著評估。

---

## 2. Cilium、Istio、Linkerd：三個不同問題的三種解法

說「哪個比較好」之前，先問清楚各自優先解的問題是什麼。

### 2.1 Istio：從控制面出發，治理需求驅動

Istio 的出發點是治理框架。它想回答的問題是：**平台團隊怎麼統一定義所有服務間通訊的安全與流量行為？**

早期 Istio 的做法是 sidecar 注入——每個 Pod 旁邊跑一個 Envoy，所有流量都過 Envoy。控制面（istiod）下發 xDS 規則，Envoy 按規執行。概念清晰，但代價是每個 Pod 多一個 sidecar，資源開銷、啟動順序、除錯複雜度都跟著上去。

2022 年之後 Istio 開始推 Ambient 模式，把 sidecar 從 Pod 層移到節點層，用 ztunnel（per-node）加上可選的 waypoint（per-namespace/service）分離 L4 與 L7。這個架構調整很重要，因為它承認了「不是每個服務都需要 L7 治理，但所有服務都需要 mTLS」。

實務上，Istio 功能覆蓋面廣、文件與社群成熟，但心智負擔重。如果你的需求是多團隊、多租戶、需要細粒度流量控制，Istio 的控制面深度是真實優勢。如果你只是想加個 mTLS，可能會發現你在操作一台 F1 賽車去買便當。

### 2.2 Cilium：從資料平面出發，網路能力驅動

Cilium 的核心不是 Mesh，是 eBPF。它先把 Linux 網路資料平面能力推到極限（kernel-native 封包處理、高效能 L3/L4 policy、封包可視性）後，然後在這個基礎上提供 Mesh 能力。

換句話說，Cilium 的 Mesh 是它網路治理能力的延伸，而不是從 Mesh 需求反推的設計。

這帶來一個很具體的差異：Cilium 對「身份」的理解是網路語意的，以 IP 和 label 為基礎，和 Istio 以 SPIFFE SVID / X.509 為基礎的身份模型不同。在需要跨叢集或跨網段的細粒度 identity-aware policy 時，兩者的表達能力有明顯差異。

適合情境：對效能、封包可視性有高要求，或者團隊本來就在深耕 Linux networking，願意把 eBPF 納入 Tech Stack。

### 2.3 Linkerd：從可維運性出發，簡潔驅動

Linkerd 的定位很明確：把最剛需的 Mesh 功能（mTLS、基礎流量指標、服務身份）做到輕、穩、好操作，其他先不管。

它的資料平面用 Rust 寫的輕量 proxy（linkerd2-proxy），記憶體佔用比 Envoy 低，啟動快，除錯體驗相對直觀。Linkerd 不追求「什麼都能做」，它追求的是「你導入之後不會後悔」。

如果你的場景是：先求穩定導入、再逐步擴展，或者團隊沒有深厚的 Mesh 操作經驗，Linkerd 是值得認真考慮的選項。

### 一句話定位

- **Istio**：我要統一治理所有服務間通訊，願意接受複雜度換取完整控制
- **Cilium**：我要把網路資料平面做到極致，Mesh 是附帶的
- **Linkerd**：我要 mTLS 和基礎指標，盡快上線、盡量少踩坑

> 延伸閱讀：Istio Ambient 架構設計文件 https://istio.io/latest/blog/2022/introducing-ambient-mesh/

---

## 3. Mesh 的 Magic 到底是什麼

第一次接觸 Mesh 的人通常會有幾個疑問：

- 應用完全沒改程式碼，流量怎麼就被接管了？
- mTLS 從哪裡來？誰在做握手？
- 怎麼能看到每條連線的延遲和策略命中？

這些看起來像魔法，但拆開來只有幾個機制，而且每個都可以獨立驗證。

以下用 miniMesh 的原始碼帶你走一遍。miniMesh 是 Ambient 風格（節點級代理）的 Service Mesh 最小實作，大約 ~1000 行 Golang 程式碼，核心資料平面更精簡。整體架構如下：

```
Node
├── minimesh-daemon (:15001)  ← 攔截所有 Pod TCP
│   ├── 同節點流量 → Unix socket relay (mTLS)
│   └── 跨節點流量 → TCP mTLS tunnel (:15000)
└── API :  /run/minimesh/api.sock  (meshctl 用)
```

### 3.1 Magic 1：應用沒改程式碼，流量怎麼被接管的？

答案在 iptables。Mesh 資料平面在節點啟動時，會在 Linux netfilter NAT table 裡插入規則，讓所有 Pod 出去的 TCP 連線「繞道」到本地 proxy port，應用完全感知不到。

miniMesh 在 [pkg/iptables/iptables.go](pkg/iptables/iptables.go) 建一條 `MINIMESH` chain，核心規則只有兩類——「skip 自己人」和「攔所有 Pod 流量」：

```go
// 1. 排除 loopback
m.t.AppendUnique(natTable, meshChain, "-i", "lo", "-j", "RETURN")

// 2. 排除 mesh 自己的兩個 port，避免代理收到自己的流量再 REDIRECT 進來
for _, port := range []string{ProxyPort, NodePort} {
    m.t.AppendUnique(natTable, meshChain,
        "-p", "tcp", "--dport", port, "-j", "RETURN")
}

// 3. 真正的攔截：來源是 podCIDR 的 TCP，全部 DNAT 到 127.0.0.1:15001
m.t.AppendUnique(natTable, meshChain,
    "-p", "tcp", "-s", podCIDR,
    "-j", "DNAT", "--to-destination", "127.0.0.1:"+ProxyPort)

// 把 MINIMESH 插在 PREROUTING 第 1 條，確保比 KUBE-SERVICES 早跑
m.t.Insert(natTable, "PREROUTING", 1, "-j", meshChain)
```

幾個 corner case 值得拆開講：

**為什麼用 `-s podCIDR`，而不是 `-d podCIDR`？**
如果只攔目的地是 Pod IP 的封包（`-d podCIDR`），那麼 `pod → Service ClusterIP` 的流量就會漏掉——因為連線剛離開 Pod 時，目的地還是 ClusterIP，不在 podCIDR 範圍內。改用「來源是 Pod」（`-s podCIDR`）就能一口氣涵蓋 pod→pod、pod→service、pod→external 三種情境。

**為什麼用 DNAT 而不是 REDIRECT？**
教科書通常教 REDIRECT，但 REDIRECT 是「把目的地改成『進來那張網卡的 IP』」。在 Calico 這類 CNI 下，Pod 對端的 `cali*` veth 在 host 側根本沒有 IP，REDIRECT 會找不到位址直接 drop。換成 DNAT 並明確指定 `127.0.0.1:15001`，就避開了這個查找。

**為什麼要 `Insert PREROUTING 1`？**
kube-proxy 在 PREROUTING 裡有一條 `KUBE-SERVICES` chain，會把 ClusterIP DNAT 成後端 Pod IP。如果 `KUBE-SERVICES` 先跑，conntrack 就把這條連線的 NAT 結果定下來了，後面我們再 DNAT 也沒用。所以必須把 `MINIMESH` 插在 PREROUTING 的最前面，搶在 kube-proxy 之前看到 ClusterIP 原貌。

**怎麼避免無窮迴圈？**
代理處理完之後會用 daemon 自己的 socket 往真正的目的地撥號，這個 Dial 的來源 IP 是 host IP（不是 podCIDR），所以 `-s podCIDR` 那條根本不會命中——天生免疫迴圈，不需要 `--uid-owner` 之類的 trick。

### 3.2 Magic 2：代理怎麼知道流量原本要去哪？

封包被改寫目的地之後，代理收到的是連往 15001 的連線，但它需要知道原本要去哪，才能正確轉發。

Linux 提供了 `SO_ORIGINAL_DST` 這個 socket option，可以從 kernel conntrack 裡拿回原始的 `sockaddr_in`。

miniMesh 在 [pkg/proxy/proxy.go](pkg/proxy/proxy.go) 裡這樣實作：

```go
func originalDst(conn net.Conn) (net.IP, uint16, error) {
    tc, ok := conn.(*net.TCPConn)
    // ...
    _, _, errno := syscall.Syscall6(
        syscall.SYS_GETSOCKOPT,
        uintptr(f.Fd()),
        syscall.IPPROTO_IP,
        soOriginalDst,          // = 80，Linux SO_ORIGINAL_DST
        uintptr(unsafe.Pointer(&sa)),
        uintptr(unsafe.Pointer(&size)),
        0,
    )
    // ...
    return net.IP(sa.Addr[:]), binary.BigEndian.Uint16(sa.Port[:]), nil
}
```

這個 syscall 拿回的 `rawSockAddrIn` 裡面就是原始目的 IP 和 port。代理拿到之後，才能做下一步的路徑決策。

### 3.3 Magic 3：Pod→Pod 與 Pod→Service 各自怎麼套 mTLS？

代理拿到原始目的地之後，第一個動作是判斷：這個 IP 屬於本節點的 Pod CIDR 嗎？這個判斷直接決定要走哪條路徑、要不要套 mTLS。

```go
func (p *Proxy) handle(ctx context.Context, conn net.Conn) {
    dstIP, dstPort, err := originalDst(conn)
    // ...
    var upstream net.Conn
    if p.localCIDR.Contains(dstIP) {
        // 同節點 Pod IP：走 Unix socket relay，套 mTLS
        upstream, err = dialRelay(p.relayPath, p.clientTLS, dstIP, dstPort)
    } else {
        // ClusterIP / 外部 IP / 跨節點 Pod：plain TCP，由 host kernel 解目的地
        upstream, err = net.Dial("tcp", fmt.Sprintf("%s:%d", dstIP, dstPort))
    }
    pipe(ctx, conn, upstream)
}
```

兩條路徑分開來看：

#### 路徑 A：Pod→Pod（同節點），完整 mTLS

`dstIP` 是同節點 Pod 的 IP（`p.localCIDR.Contains(dstIP) == true`），代理走 `dialRelay`：

```go
func dialRelay(path string, cfg *tls.Config, ip net.IP, port uint16) (net.Conn, error) {
    raw, _ := net.Dial("unix", path)              // 1. 連到本地 Unix socket
    tc := tls.Client(raw, cfg)
    if err := tc.Handshake(); err != nil { ... }  // 2. 在 Unix socket 上做 mTLS 握手
    hdr := make([]byte, 6)                        // 3. 寫 6-byte 目的地 header
    copy(hdr[:4], ip.To4())
    binary.BigEndian.PutUint16(hdr[4:], port)
    tc.Write(hdr)
    return tc, nil
}
```

對端是同個 daemon 裡的 `Relay`，從 Unix socket 上做 TLS server-side 握手、讀那 6 byte、再 dial 真正的 Pod：

```go
func (r *Relay) handleRelay(ctx context.Context, raw net.Conn) {
    conn := tls.Server(raw, r.serverTLS)
    conn.Handshake()                              // mTLS 對接
    hdr := make([]byte, 6)
    io.ReadFull(conn, hdr)
    ip, port := net.IP(hdr[:4]), binary.BigEndian.Uint16(hdr[4:])
    upstream, _ := net.Dial("tcp", fmt.Sprintf("%s:%d", ip, port))
    pipe(ctx, conn, upstream)
}
```

整條鏈是：`client pod → iptables DNAT → Proxy(15001) → tls.Client → Unix socket → tls.Server(Relay) → server pod`。
中間 `tls.Client` 和 `tls.Server` 用的是同一份 CA 簽出來的憑證，所以兩端都能互相驗身份——這就是 mTLS。

> 為什麼用 Unix socket 而不是 TCP？同節點通訊不需要走網路 stack，Unix socket 比 loopback TCP 更便宜；但我們仍然在它上面包 TLS，目的不是加密傳輸（反正沒上線），而是**做身份驗證**——確保只有持有合法 client cert 的對象才能接到 Relay。這是 Ambient Mesh 與 sidecar 模式都共用的原則：mTLS 的價值首先是 identity，加密只是順便。

#### 路徑 B：Pod→Service（ClusterIP），由 kube-proxy 接手

`dstIP` 是 ClusterIP，**不在 podCIDR**，所以走 else 分支的 `net.Dial`。這裡有兩個值得展開的細節：

**(1) 為什麼 ClusterIP 能正確抵達後端 Pod？**

代理 `net.Dial("tcp", "<ClusterIP>:<port>")` 是從 daemon process 發出的，連線進入 host 的 OUTPUT chain，被 kube-proxy 的 `KUBE-SERVICES` 規則 DNAT 成某個後端 Pod IP。換句話說：**PREROUTING 階段我們搶在 kube-proxy 前面攔下原始 ClusterIP，OUTPUT 階段又把它交還給 kube-proxy 做 load balancing**。職責分工很乾淨。

**(2) 這條路徑現階段沒有 mTLS——為什麼？**

可以從程式碼直接看到 else 分支只用了 `net.Dial`，沒有 `tls.Client`。原因是：在 `Proxy.handle` 看到的 dstIP 是 ClusterIP，我們不知道它最後會被 kube-proxy 解成哪一個後端 Pod，也不知道那個 Pod 是不是 mesh 成員、它的 daemon 監聽在哪個 NodePort。要做完整 pod→service mTLS，需要再加兩塊東西：

- **服務探索**：監聽 K8s API，把 ClusterIP → EndpointSlice → 後端 Pod IP / 所在節點 IP 的對應關係維護在記憶體裡。
- **路徑切換**：如果後端 Pod 在本節點，走 Relay；在其他節點，改打 `tls.Dial("tcp", "<remoteNodeIP>:15000", clientTLS)` 走 NodePort tunnel。

程式碼裡的 `// TODO: for multi-node mesh, look up remote pod CIDRs from the K8s API` 那段註解就是預留給這件事的鉤子。Istio Ambient 的 ztunnel 在做的，本質上也是這套查表 + 路徑切換邏輯，只是表更大、更動態。

**小結這兩條路徑的差別：**

| 路徑 | iptables 攔截 | proxy 判斷 | 上游連線 | mTLS |
|---|---|---|---|---|
| Pod → 同節點 Pod | DNAT 到 :15001 | dstIP ∈ podCIDR | Unix socket → Relay | ✅ Client + Server 雙向 |
| Pod → Service ClusterIP | DNAT 到 :15001 | dstIP ∉ podCIDR | net.Dial(ClusterIP)，由 kube-proxy DNAT | ❌ 目前是 plain TCP，待補服務探索 |

> 重點：「能不能對 Service 套 mTLS」不是 iptables 或 SO_ORIGINAL_DST 的問題——攔截早就成功了。真正的瓶頸是**控制面要把 ClusterIP 翻譯成可信任的後端身份**。這也是為什麼 Istio、Linkerd 都要花大量篇幅處理 EndpointSlice、SPIFFE identity、locality routing——資料平面的攔截一兩百行就寫完，控制面查表才是工程量所在。

### 3.4 Magic 4：mTLS 從哪來？自己簽的 CA

這是最常被誤會的部分。mTLS 聽起來很複雜，但底層就是兩個 TLS config——一個要求驗 client cert，一個帶著 client cert 去驗 server。

miniMesh 在 [pkg/cert/cert.go](pkg/cert/cert.go) 啟動時建一個 in-memory self-signed CA，然後 issue 工作憑證：

```go
// CA：self-signed，有效 10 年，只在記憶體裡
func NewCA() (*CA, error) {
    key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    tmpl := &x509.Certificate{
        IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
        // ...
    }
    // ...
}

// 工作憑證：有效 24 小時，綁定 DNS name
func (ca *CA) Issue(dnsName string) (*Bundle, error) { ... }

// Server 要求並驗證 client cert，最低 TLS 1.3
func (ca *CA) ServerTLS(b *Bundle) *tls.Config {
    return &tls.Config{
        Certificates: []tls.Certificate{b.TLSCert},
        ClientAuth:   tls.RequireAndVerifyClientCert,
        ClientCAs:    ca.pool,
        MinVersion:   tls.VersionTLS13,
    }
}
```

這幾段加起來就是「兩端都用同一個 CA 簽的憑證，握手時互相驗」。生產環境會換成 SPIRE 或 cert-manager 管理憑證，但機制完全一樣。

### 3.5 Magic 5：控制面怎麼運作的？

控制面負責把「宣告的期望狀態」轉換成「實際生效的資源」。miniMesh 用兩個 Kubernetes controller 做示範。

在 [operator/controller/controller.go](operator/controller/controller.go)，`MeshCertificateReconciler` 每次 reconcile 就用 CA 簽一張新憑證，存進 Kubernetes Secret，並設定 12 小時後再回來輪替：

```go
func (r *MeshCertificateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // ...
    bundle, err := r.CA.Issue(dnsName)
    // 存進 kubernetes.io/tls Secret
    controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
        secret.Data = map[string][]byte{
            corev1.TLSCertKey:       bundle.CertPEM,
            corev1.TLSPrivateKeyKey: bundle.KeyPEM,
        }
        return nil
    })
    // 12 小時後自動重新觸發
    return ctrl.Result{RequeueAfter: 12 * time.Hour}, nil
}
```

這就是控制面最小閉環的樣子：CRD 宣告 desired state → reconcile 實際資源 → 狀態寫回 CR status。

### 3.6 Magic 6：可觀測怎麼接？

[pkg/observability/pwru.go](pkg/observability/pwru.go) 做的事很簡單——把 Cilium 的 `pwru`（eBPF 封包追蹤工具）包起來，讓 `meshctl observe` 可以直接串流 kernel 層的封包事件到終端機。

這裡有個值得記住的設計選擇：**可觀測不一定要先建 telemetry pipeline**。`pwru` 是 eBPF-based，可以在 kernel 層追蹤每個封包走過哪些 netfilter hook、哪個 function、最後去了哪，不需要應用配合，不需要 sidecar。先把關鍵路徑看見，再決定要不要接 Prometheus / Jaeger。

> 延伸閱讀：Cilium pwru https://github.com/cilium/pwru

---

## 4. 你自己要做的話，從哪裡開始

如果目標是理解 Mesh 而不是直接生產部署，建議從最小切片出發，不要一開始就追完整 Mesh。

### 最小可運作版本需要什麼

四個能力做完就是最小 Mesh：

1. **流量攔截**：iptables REDIRECT + 排除自身流量
2. **原始目的地還原**：SO_ORIGINAL_DST
3. **雙向透明轉發**：`io.Copy` bidirectional pipe
4. **mTLS 握手**：自簽 CA + TLS 1.3 mutual auth

miniMesh 核心程式碼量：
- [pkg/iptables/iptables.go](pkg/iptables/iptables.go)：攔截規則，約 70 行
- [pkg/cert/cert.go](pkg/cert/cert.go)：最小 CA / mTLS，約 130 行
- [pkg/proxy/proxy.go](pkg/proxy/proxy.go)：透明代理 + Unix relay，約 240 行

加總 440 行左右，這就是「幾百行」的意思。

### 建議順序

不要一開始就接 Kubernetes，先在本機跑通再說：

1. 單機跑通 iptables REDIRECT + SO_ORIGINAL_DST，用 `curl` 驗流量有沒有過代理
2. 加 bidirectional pipe，驗連線能正常收發資料
3. 補 mTLS，驗 `openssl s_client` 能完成握手並看到 client cert
4. 加最小 HTTP API 和健康檢查，為 DaemonSet 做準備
5. 最後才接 CRD 和 controller

> 注意：控制面複雜度很容易在早期把注意力吃掉。先把資料平面跑通，再想控制面要管什麼。

---

## 5. 常見踩坑

### 流量無窮迴圈，CPU 飆高

現象：代理啟動後 CPU 立刻滿載，連線完全卡死。

原因：iptables 沒有排除 daemon 自身的流量，daemon 自己發出的連線被 REDIRECT 回自己，形成迴圈。

修法：確認 `--uid-owner` 規則有正確設定，或用 `--pid-owner` 排除 daemon 的 PID。可以用 `iptables -t nat -L MINIMESH -v` 確認規則是否生效。

### mTLS 握手一直失敗

現象：TLS handshake error，log 顯示 `certificate signed by unknown authority` 或 `bad certificate`。

先確認：
- `ClientTLS` 裡的 `ServerName` 和對方憑證的 SAN 是否一致
- 兩端是不是用同一個 CA pool
- `MinVersion` 兩邊是否都是 TLS 1.3

可以用 `openssl s_client -connect <ip>:<port> -cert client.pem -key client.key` 手動驗握手流程。

### 代理轉發失敗，`dial <ip>:<port>: connection refused`

現象：SO_ORIGINAL_DST 拿到了，但 dial 目的地失敗。

先確認：
- 用 `getsockopt` 拿回的 IP:Port 是否正確（可以 log 出來比對）
- relay 的 6-byte header 有沒有正確寫入
- 目的 Pod 是否真的在監聽那個 port

---

## 小結

回到開頭的問題：Service Mesh 到底在解決什麼問題？

它解的是**服務間通訊的治理問題**，不是某個技術問題。技術手段（iptables、mTLS、eBPF）都是為了讓「誰負責加密、誰負責策略、誰負責可觀測」這件事能夠從應用層抽離、從平台層統一管理。

Cilium、Istio、Linkerd 的差別不在功能清單，在於它們各自優先解的問題域不同。選擇之前，值得先問：我的問題是治理需求、效能需求，還是快速落地需求？

而 Mesh 背後的「魔法」——iptables REDIRECT、SO_ORIGINAL_DST、mTLS 握手、Kubernetes reconcile loop——每一個都是可以獨立拆開、單獨驗證的工程機制。miniMesh 這個幾百行的專案，就是把這些機制串起來的最小版本，很適合作為理解 Mesh 的第一個動手點。

---

## 參考來源

- miniMesh 原始碼：[pkg/proxy/proxy.go](pkg/proxy/proxy.go) · [pkg/iptables/iptables.go](pkg/iptables/iptables.go) · [pkg/cert/cert.go](pkg/cert/cert.go) · [pkg/daemon/daemon.go](pkg/daemon/daemon.go) · [operator/controller/controller.go](operator/controller/controller.go)
- [README.md](README.md)
- Istio Ambient Mesh 設計：https://istio.io/latest/blog/2022/introducing-ambient-mesh/
- Cilium pwru（eBPF 封包追蹤）：https://github.com/cilium/pwru
- Linux `SO_ORIGINAL_DST`：`man 7 netfilter`
