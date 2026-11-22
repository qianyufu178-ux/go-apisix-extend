module github.com/yfyadmin/go-apisix-extend

go 1.18

require gopkg.in/yaml.v3 v3.0.1

// 离线构建适配：go.yaml.in/yaml/v3 是 gopkg.in/yaml.v3 的官方新家（同一份代码、API 完全兼容）。
// 部分内网 / 无外网环境只缓存了前者，这里做一次路径替换，保证 `go build ./...` 开箱即用。
replace gopkg.in/yaml.v3 => go.yaml.in/yaml/v3 v3.0.5
