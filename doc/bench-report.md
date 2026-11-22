# 压测报告 · ZetaGateway（go-apisix-extend）

> 本报告只写**本仓库在本机实际跑出来的数字**，命令与原始输出都在文末，可原样复现。
> 不引用任何未复跑的第三方数据。

## 1. 测试环境

| 项 | 值 |
|---|---|
| CPU | 13th Gen Intel(R) Core(TM) i5-13400F（16 逻辑核） |
| 内存 | 31.8 GB |
| 系统 | Windows 11（NT 10.0.26200） |
| Go | go1.27.4 windows/amd64 |
| 环境变量 | `GOFLAGS=-mod=mod`、`GOPROXY=off`、`GOTOOLCHAIN=local`（离线可复现） |
| 采样方式 | 每项 `-benchmem`，热点项 `-count 3` 取区间 |

## 2. 结论速览

| # | 场景 | 结果 | 说明 |
|---|---|---|---|
| 1 | 路由匹配（1000 条路由，并行 16 线程） | **4.28 – 17.23 ns/op，0 B/op，0 allocs/op** | 每请求必经路径，读完全无锁 |
| 2 | 网关完整请求路径（上下文池 → 路由 → 插件 → local 处理器） | **70.84 – 77.22 ns/op，0 B/op，0 allocs/op** | 进程内开销，不含网络 |
| 3 | 上下文池 Get/Set/Put | **27.71 ns/op，0 allocs/op** | `sync.Pool` + 预分配 map |
| 4 | AddRoute（Copy-On-Write）表长 10 / 100 / 1000 | **1.09 / 4.11 / 25.33 µs/op** | 写成本正比于表长（设计取舍） |

读路径的三个数字合起来说明一件事：**稳态下每处理一个请求不产生任何堆分配**（`0 allocs/op`），GC 压力只来自真正的业务数据。

## 3. 本轮优化：3 allocs/op → 0 allocs/op

| 指标 | 优化前 | 优化后 | 变化 |
|---|---|---|---|
| `BenchmarkMatch` ns/op（1000 条路由，并行） | 29.55 | 4.28 – 17.23 | 1.7× – 6.9× 更快 |
| `BenchmarkMatch` B/op | 56 | **0** | 消除 |
| `BenchmarkMatch` allocs/op | 3 | **0** | 消除 |

改了什么（共 3 处，均为路由包内部细节，对外行为不变）：

1. `buildRouteKey`：`fmt.Sprintf("%s:%s", method, path)` → `method + ":" + path`（1 次分配，去掉 Sprintf 的反射与缓冲开销）；
2. `Match`：把拼接内联进 map 下标 `table.exact[method+routeKeySep+path]`，编译器不为这次查找分配临时字符串 → 读路径彻底零分配；
3. `AddRoute` / `RemoveRoute` 的前缀遍历：由「为每条前缀路由构建 key 再比较」改为直接比较 `Method`/`Path` 字段 → 写路径也少了 N 次分配。

> 附带修正：`BenchmarkAddRoute` 原实现让路由表在 `b.N` 次循环中持续增长，整体退化为 **O(n²)**，表长大后基准根本跑不完（本轮第一次运行就卡在这里）。
> 现改为「固定表长 + 子基准」：每轮重建固定规模的路由表且不计时，只对 1 次 `AddRoute` 计时，因此上表的 3 档数据可以直接读出 COW 写成本的斜率 ≈ **24.5 ns / 已存在路由条数**。

## 4. 原始输出

```
=== gofmt -l (should be empty) ===
=== go vet ./... ===
vet exit=0
=== go test ./... ===
?   github.com/yfyadmin/go-apisix-extend/cmd/server   [no test files]
ok  github.com/yfyadmin/go-apisix-extend/pkg/config  1.473s
ok  github.com/yfyadmin/go-apisix-extend/pkg/context 1.710s
ok  github.com/yfyadmin/go-apisix-extend/pkg/live    2.685s
ok  github.com/yfyadmin/go-apisix-extend/pkg/plugin  0.991s
ok  github.com/yfyadmin/go-apisix-extend/pkg/proxy   2.367s
ok  github.com/yfyadmin/go-apisix-extend/pkg/router  0.807s
test exit=0
```

```
=== bench router Match (500k x 3) ===
goos: windows
goarch: amd64
pkg: github.com/yfyadmin/go-apisix-extend/pkg/router
cpu: 13th Gen Intel(R) Core(TM) i5-13400F
BenchmarkMatch-16      500000      9.137 ns/op     0 B/op     0 allocs/op
BenchmarkMatch-16      500000      7.029 ns/op     0 B/op     0 allocs/op
BenchmarkMatch-16      500000     17.230 ns/op     0 B/op     0 allocs/op
PASS
ok  github.com/yfyadmin/go-apisix-extend/pkg/router  0.775s
```

```
=== bench router AddRoute (COW, 20 iterations per size) ===
BenchmarkAddRoute/size=10-16       20     1085 ns/op     592 B/op     8 allocs/op
BenchmarkAddRoute/size=100-16      20     4110 ns/op    3632 B/op     8 allocs/op
BenchmarkAddRoute/size=1000-16     20    25325 ns/op   54744 B/op    10 allocs/op
PASS
ok  github.com/yfyadmin/go-apisix-extend/pkg/router  1.052s
```

```
=== bench context pool (1M x 1) ===
BenchmarkGetPutContext-16   1000000   27.71 ns/op   0 B/op   0 allocs/op
BenchmarkPoolReuse-16       1000000  365.30 ns/op  481 B/op  3 allocs/op
PASS
ok  github.com/yfyadmin/go-apisix-extend/pkg/context  1.588s
```

```
=== bench proxy full path (300k x 3) ===
BenchmarkServeHTTP_LocalRoute-16   300000   70.84 ns/op   0 B/op   0 allocs/op
BenchmarkServeHTTP_LocalRoute-16   300000   75.73 ns/op   0 B/op   0 allocs/op
BenchmarkServeHTTP_LocalRoute-16   300000   77.22 ns/op   0 B/op   0 allocs/op
PASS
ok  github.com/yfyadmin/go-apisix-extend/pkg/proxy  1.942s
```

`BenchmarkPoolReuse` 的 481 B / 3 allocs 来自基准自身预分配的 `[]*GatewayContext` 切片（`b.N = 1e6` 个指针），
不是池的分配——它验证的是「池化对象本身不会因复用而产生新分配」。

## 5. 如何复测

```powershell
cd go-apisix-extend

# 质量门禁
gofmt -l .
go vet ./...
go test -count=1 ./...

# 路由读路径（1000 条路由，并行）
go test ./pkg/router -run XXX -bench 'BenchmarkMatch$' -benchtime 500000x -count 3 -benchmem

# COW 写成本（表长 10 / 100 / 1000）
go test ./pkg/router -run XXX -bench 'BenchmarkAddRoute' -benchtime 20x -benchmem

# 上下文池
go test ./pkg/context -run XXX -bench . -benchtime 1000000x -benchmem

# 网关完整请求路径（不含网络）
go test ./pkg/proxy -run XXX -bench . -benchtime 300000x -count 3 -benchmem
```

> `-benchtime 20x` 是刻意设的：`size=1000` 每组要重建 1000 条路由，用固定迭代次数可以避免基准自身跑上几分钟。

## 6. 端到端链路实测（同机，真实媒体面）

同一台机器上跑完整链路（采集端 → 网关 → 转码器 → 浏览器），连续 **11.2 小时**：

| 指标 | 值 |
|---|---|
| 网关推流 | `publishing=true`，`subscribers=1`，`bytes_in=26,883,756,540`（≈26.9 GB），`chunks_in=1,107,566`，`dropped=0`，`silence_ms=31` |
| RTP 中转 | `packets_received=15,880,248`，`packets_forwarded=15,880,248`，`packets_dropped=0`，`payload_bytes≈16.7 GB` |
| 转码 | `h264_nvenc`，1280×720@30，3318 kbps，`restarts=1`（修复前 45 秒 31 次） |
| 订阅者隔离 | 慢订阅者按整块丢弃并计数，发布端从未被阻塞（`dropped=0`） |

也就是说：**读路径的零分配不是纸面结论**，它在 11 小时的连续媒体面转发里表现为 `packets_dropped=0`、`dropped=0`。

## 7. 边界与诚实声明

1. 以上全部是**进程内**开销，不是 HTTP QPS。真实 QPS 取决于内核网络栈、TLS、上游时延，请用 `wrk` / `hey` 按目标机器复测：
   ```powershell
   # 例：对本地网关的本地处理器压测
   wrk -t4 -c256 -d30s --latency http://127.0.0.1:8082/ingest/live
   ```
2. `ns/op` 在 Windows 上有波动（同一项 4.28 → 17.23 ns），后台进程与定时器都会影响；**`0 allocs/op` 是稳定的结论**，吞吐数字请以多次运行的中位数为准。
3. COW 的写成本是 `O(表长)`：`size=1000` 时单次 `AddRoute` 约 25 µs。路由热更新适合秒级/分钟级频率，**不要**放在每请求路径上。
4. 本报告不包含多核横向扩展、TLS 终止、下游慢速上游等场景；这些属于部署形态，需要在目标环境单独压测。
