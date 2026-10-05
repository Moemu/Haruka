# 第三方声明

Haruka 的 Apache-2.0 许可只适用于本项目内容，第三方依赖保留原许可。下表根据当前 Go 模块图整理，附带依赖模块公开的许可、版权和其他来源声明。升级依赖时需要重新核对。

| 模块 | 版本 | 上游声明副本 |
| --- | --- | --- |
| `github.com/dustin/go-humanize` | `v1.0.1` | [LICENSE](licenses/github.com_dustin_go-humanize/LICENSE) |
| `github.com/google/pprof` | `v0.0.0-20260802141513-ef3492d7dac3` | [LICENSE](licenses/github.com_google_pprof/LICENSE) |
| `github.com/google/uuid` | `v1.6.0` | [LICENSE](licenses/github.com_google_uuid/LICENSE) |
| `github.com/hashicorp/golang-lru/v2` | `v2.0.7` | [LICENSE](licenses/github.com_hashicorp_golang-lru_v2/LICENSE) |
| `github.com/lionsoul2014/ip2region/binding/golang` | `v0.0.0-20260901011515-c1a1fc7d5941` | [LICENSE.md](licenses/github.com_lionsoul2014_ip2region_binding_golang/LICENSE.md) |
| `github.com/mattn/go-isatty` | `v0.0.24` | [LICENSE](licenses/github.com_mattn_go-isatty/LICENSE) |
| `github.com/ncruces/go-strftime` | `v1.0.0` | [LICENSE](licenses/github.com_ncruces_go-strftime/LICENSE) |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | [LICENSE](licenses/github.com_remyoudompheng_bigfft/LICENSE) |
| `golang.org/x/mod` | `v0.41.0` | [LICENSE](licenses/golang.org_x_mod/LICENSE) |
| `golang.org/x/sync` | `v0.23.0` | [LICENSE](licenses/golang.org_x_sync/LICENSE) |
| `golang.org/x/sys` | `v0.48.0` | [LICENSE](licenses/golang.org_x_sys/LICENSE) |
| `golang.org/x/tools` | `v0.50.0` | [LICENSE](licenses/golang.org_x_tools/LICENSE) |
| `gopkg.in/check.v1` | `v0.0.0-20161208181325-20d25e280405` | [LICENSE](licenses/gopkg.in_check.v1/LICENSE) |
| `gopkg.in/yaml.v3` | `v3.0.1` | [LICENSE](licenses/gopkg.in_yaml.v3/LICENSE)、[NOTICE](licenses/gopkg.in_yaml.v3/NOTICE) |
| `modernc.org/cc/v4` | `v4.29.7` | [LICENSE](licenses/modernc.org_cc_v4/LICENSE) |
| `modernc.org/ccgo/v4` | `v4.36.1` | [LICENSE](licenses/modernc.org_ccgo_v4/LICENSE) |
| `modernc.org/fileutil` | `v1.4.0` | [LICENSE](licenses/modernc.org_fileutil/LICENSE) |
| `modernc.org/gc/v2` | `v2.6.5` | [LICENSE](licenses/modernc.org_gc_v2/LICENSE) |
| `modernc.org/gc/v3` | `v3.1.5` | [LICENSE](licenses/modernc.org_gc_v3/LICENSE) |
| `modernc.org/goabi0` | `v0.2.0` | [LICENSE](licenses/modernc.org_goabi0/LICENSE) |
| `modernc.org/libc` | `v1.77.1` | [LICENSE](licenses/modernc.org_libc/LICENSE)、[LICENSE-3RD-PARTY.md](licenses/modernc.org_libc/LICENSE-3RD-PARTY.md) |
| `modernc.org/mathutil` | `v1.7.1` | [LICENSE](licenses/modernc.org_mathutil/LICENSE) |
| `modernc.org/memory` | `v1.12.1` | [LICENSE](licenses/modernc.org_memory/LICENSE)、[LICENSE-GO](licenses/modernc.org_memory/LICENSE-GO)、[LICENSE-LOGO](licenses/modernc.org_memory/LICENSE-LOGO)、[LICENSE-MMAP-GO](licenses/modernc.org_memory/LICENSE-MMAP-GO) |
| `modernc.org/opt` | `v0.2.0` | [LICENSE](licenses/modernc.org_opt/LICENSE) |
| `modernc.org/sortutil` | `v1.2.1` | [LICENSE](licenses/modernc.org_sortutil/LICENSE) |
| `modernc.org/sqlite` | `v1.60.1` | [LICENSE](licenses/modernc.org_sqlite/LICENSE)、[LICENSE-3RD-PARTY.md](licenses/modernc.org_sqlite/LICENSE-3RD-PARTY.md)、[LICENSE-SQLITE](licenses/modernc.org_sqlite/LICENSE-SQLITE)、[LICENSE-SQLITE_VEC](licenses/modernc.org_sqlite/LICENSE-SQLITE_VEC) |
| `modernc.org/strutil` | `v1.2.1` | [LICENSE](licenses/modernc.org_strutil/LICENSE) |
| `modernc.org/token` | `v1.1.0` | [LICENSE](licenses/modernc.org_token/LICENSE) |

ip2region 绑定采用 Apache-2.0；Go 系统库采用 BSD 许可；yaml.v3 同时包含 MIT 和 Apache 许可文件；modernc SQLite 及其底层来源以附带声明为准。复制声明不表示 Haruka 修改或包含了所有上游源码。

离线 `.xdb` 数据库不随仓库提供，使用者须自行取得并遵守数据源许可。状态页默认从 jsDelivr 加载 `lxgw-wenkai-screen-webfont@1.7.0`。字体也不随仓库提供，许可及来源见 [LXGW WenKai Screen](https://github.com/lxgw/LxgwWenKai-Screen)；字体包加载会向 CDN 发送访客网络请求。需要完全本地部署时，可自行托管字体并修改页面链接及 CSP。

发布二进制时，请一并提供本项目 `LICENSE`、`NOTICE`、本声明和 `licenses/`，并核对构建工具链和最终依赖的再分发要求。
