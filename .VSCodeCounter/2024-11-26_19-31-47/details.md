# Details

Date : 2024-11-26 19:31:47

Directory /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts

Total : 192 files,  19880 codes, 3403 comments, 4056 blanks, all 27339 lines

[Summary](results.md) / Details / [Diff Summary](diff.md) / [Diff Details](diff-details.md)

## Files
| filename | language | code | comment | blank | total |
| :--- | :--- | ---: | ---: | ---: | ---: |
| [volts/README.md](/volts/README.md) | Markdown | 66 | 0 | 12 | 78 |
| [volts/broker/README.md](/volts/broker/README.md) | Markdown | 1 | 0 | 0 | 1 |
| [volts/broker/broker.go](/volts/broker/broker.go) | Go | 70 | 8 | 15 | 93 |
| [volts/broker/config.go](/volts/broker/config.go) | Go | 172 | 31 | 40 | 243 |
| [volts/broker/http/event.go](/volts/broker/http/event.go) | Go | 23 | 0 | 7 | 30 |
| [volts/broker/http/handler.go](/volts/broker/http/handler.go) | Go | 38 | 3 | 12 | 53 |
| [volts/broker/http/http.go](/volts/broker/http/http.go) | Go | 402 | 131 | 112 | 645 |
| [volts/broker/http/http_test.go](/volts/broker/http/http_test.go) | Go | 308 | 1 | 76 | 385 |
| [volts/broker/http/subscriber.go](/volts/broker/http/subscriber.go) | Go | 24 | 0 | 6 | 30 |
| [volts/broker/memory/memory.go](/volts/broker/memory/memory.go) | Go | 199 | 3 | 45 | 247 |
| [volts/broker/memory/memory_test.go](/volts/broker/memory/memory_test.go) | Go | 39 | 0 | 12 | 51 |
| [volts/broker/message.go](/volts/broker/message.go) | Go | 5 | 1 | 2 | 8 |
| [volts/client/README.md](/volts/client/README.md) | Markdown | 0 | 0 | 1 | 1 |
| [volts/client/client.go](/volts/client/client.go) | Go | 35 | 37 | 9 | 81 |
| [volts/client/config.go](/volts/client/config.go) | Go | 290 | 83 | 61 | 434 |
| [volts/client/http_client.go](/volts/client/http_client.go) | Go | 374 | 76 | 74 | 524 |
| [volts/client/http_client_test.go](/volts/client/http_client_test.go) | Go | 100 | 4 | 15 | 119 |
| [volts/client/http_request.go](/volts/client/http_request.go) | Go | 105 | 16 | 23 | 144 |
| [volts/client/http_response.go](/volts/client/http_response.go) | Go | 41 | 2 | 14 | 57 |
| [volts/client/http_roundtripper.go](/volts/client/http_roundtripper.go) | Go | 98 | 29 | 20 | 147 |
| [volts/client/retry.go](/volts/client/retry.go) | Go | 24 | 4 | 8 | 36 |
| [volts/client/rpc_client.go](/volts/client/rpc_client.go) | Go | 254 | 65 | 49 | 368 |
| [volts/client/rpc_request.go](/volts/client/rpc_request.go) | Go | 82 | 2 | 17 | 101 |
| [volts/client/rpc_response.go](/volts/client/rpc_response.go) | Go | 24 | 0 | 6 | 30 |
| [volts/client/rpc_stream.go](/volts/client/rpc_stream.go) | Go | 114 | 54 | 37 | 205 |
| [volts/cmd/cmd.go](/volts/cmd/cmd.go) | Go | 1 | 0 | 1 | 2 |
| [volts/codec/bytes.go](/volts/codec/bytes.go) | Go | 34 | 6 | 11 | 51 |
| [volts/codec/codec.go](/volts/codec/codec.go) | Go | 46 | 6 | 11 | 63 |
| [volts/codec/codec_test.go](/volts/codec/codec_test.go) | Go | 20 | 0 | 5 | 25 |
| [volts/codec/gob.go](/volts/codec/gob.go) | Go | 34 | 2 | 10 | 46 |
| [volts/codec/json.go](/volts/codec/json.go) | Go | 22 | 8 | 9 | 39 |
| [volts/codec/json_test.go](/volts/codec/json_test.go) | Go | 28 | 2 | 5 | 35 |
| [volts/codec/msgpack.go](/volts/codec/msgpack.go) | Go | 15 | 5 | 8 | 28 |
| [volts/codec/msgpack_test.go](/volts/codec/msgpack_test.go) | Go | 27 | 3 | 5 | 35 |
| [volts/codec/protobuf.go](/volts/codec/protobuf.go) | Go | 28 | 3 | 12 | 43 |
| [volts/config.go](/volts/config.go) | Go | 97 | 17 | 21 | 135 |
| [volts/config/config.go](/volts/config/config.go) | Go | 263 | 44 | 68 | 375 |
| [volts/config/config_test.go](/volts/config/config_test.go) | Go | 72 | 2 | 16 | 90 |
| [volts/config/config_test.json](/volts/config/config_test.json) | JSON | 7 | 0 | 0 | 7 |
| [volts/config/const.go](/volts/config/const.go) | Go | 18 | 0 | 5 | 23 |
| [volts/config/format.go](/volts/config/format.go) | Go | 127 | 4 | 20 | 151 |
| [volts/config/option.go](/volts/config/option.go) | Go | 43 | 5 | 8 | 56 |
| [volts/demo/handler_call_test.go](/volts/demo/handler_call_test.go) | Go | 97 | 10 | 25 | 132 |
| [volts/demo/rpc/hello_world/client/main.go](/volts/demo/rpc/hello_world/client/main.go) | Go | 15 | 1 | 4 | 20 |
| [volts/demo/rpc/hello_world/config.ini](/volts/demo/rpc/hello_world/config.ini) | Ini | 16 | 0 | 5 | 21 |
| [volts/demo/rpc/hello_world/main.go](/volts/demo/rpc/hello_world/main.go) | Go | 47 | 1 | 13 | 61 |
| [volts/demo/web/hello_world/go.mod](/volts/demo/web/hello_world/go.mod) | Go Module File | 5 | 0 | 2 | 7 |
| [volts/demo/web/hello_world/main.go](/volts/demo/web/hello_world/main.go) | Go | 48 | 2 | 11 | 61 |
| [volts/demo/web/volts_middleware/config.ini](/volts/demo/web/volts_middleware/config.ini) | Ini | 13 | 0 | 2 | 15 |
| [volts/demo/web/volts_middleware/main.go](/volts/demo/web/volts_middleware/main.go) | Go | 37 | 6 | 11 | 54 |
| [volts/demo/web/volts_module/config.ini](/volts/demo/web/volts_module/config.ini) | Ini | 0 | 0 | 1 | 1 |
| [volts/demo/web/volts_module/group/base/base.go](/volts/demo/web/volts_module/group/base/base.go) | Go | 9 | 0 | 4 | 13 |
| [volts/demo/web/volts_module/group/base/controller.go](/volts/demo/web/volts_module/group/base/controller.go) | Go | 9 | 0 | 4 | 13 |
| [volts/demo/web/volts_module/group/base/static/index.css](/volts/demo/web/volts_module/group/base/static/index.css) | CSS | 3 | 0 | 0 | 3 |
| [volts/demo/web/volts_module/group/base/template/index.html](/volts/demo/web/volts_module/group/base/template/index.html) | HTML | 12 | 0 | 0 | 12 |
| [volts/demo/web/volts_module/group/web/web.go](/volts/demo/web/volts_module/group/web/web.go) | Go | 9 | 0 | 4 | 13 |
| [volts/demo/web/volts_module/main.go](/volts/demo/web/volts_module/main.go) | Go | 22 | 1 | 5 | 28 |
| [volts/demo/web/volts_restful/config.ini](/volts/demo/web/volts_restful/config.ini) | Ini | 0 | 0 | 1 | 1 |
| [volts/demo/web/volts_restful/main.go](/volts/demo/web/volts_restful/main.go) | Go | 32 | 3 | 9 | 44 |
| [volts/doc.go](/volts/doc.go) | Go | 1 | 21 | 2 | 24 |
| [volts/go.mod](/volts/go.mod) | Go Module File | 83 | 1 | 6 | 90 |
| [volts/go.sum](/volts/go.sum) | Go Checksum File | 804 | 0 | 1 | 805 |
| [volts/handler/http/gateway.go](/volts/handler/http/gateway.go) | Go | 1 | 1 | 2 | 4 |
| [volts/internal/acme/acme.go](/volts/internal/acme/acme.go) | Go | 17 | 7 | 5 | 29 |
| [volts/internal/acme/autocert/autocert.go](/volts/internal/acme/autocert/autocert.go) | Go | 34 | 7 | 8 | 49 |
| [volts/internal/acme/autocert/autocert_test.go](/volts/internal/acme/autocert/autocert_test.go) | Go | 10 | 4 | 3 | 17 |
| [volts/internal/acme/autocert/cache.go](/volts/internal/acme/autocert/cache.go) | Go | 33 | 1 | 4 | 38 |
| [volts/internal/acme/options.go](/volts/internal/acme/options.go) | Go | 51 | 24 | 12 | 87 |
| [volts/internal/addr/addr.go](/volts/internal/addr/addr.go) | Go | 130 | 19 | 28 | 177 |
| [volts/internal/addr/addr_test.go](/volts/internal/addr/addr_test.go) | Go | 68 | 0 | 12 | 80 |
| [volts/internal/avatar/avatar.go](/volts/internal/avatar/avatar.go) | Go | 41 | 11 | 8 | 60 |
| [volts/internal/avatar/avatar_test.go](/volts/internal/avatar/avatar_test.go) | Go | 28 | 0 | 6 | 34 |
| [volts/internal/backoff/backoff.go](/volts/internal/backoff/backoff.go) | Go | 11 | 3 | 3 | 17 |
| [volts/internal/body/body.go](/volts/internal/body/body.go) | Go | 124 | 12 | 31 | 167 |
| [volts/internal/body/body_test.go](/volts/internal/body/body_test.go) | Go | 56 | 3 | 13 | 72 |
| [volts/internal/buf/buf.go](/volts/internal/buf/buf.go) | Go | 17 | 0 | 5 | 22 |
| [volts/internal/errors/errors.go](/volts/internal/errors/errors.go) | Go | 71 | 7 | 11 | 89 |
| [volts/internal/header/header.go](/volts/internal/header/header.go) | Go | 5 | 4 | 3 | 12 |
| [volts/internal/mdns/client.go](/volts/internal/mdns/client.go) | Go | 389 | 52 | 65 | 506 |
| [volts/internal/mdns/dns_sd.go](/volts/internal/mdns/dns_sd.go) | Go | 25 | 54 | 6 | 85 |
| [volts/internal/mdns/dns_sd_test.go](/volts/internal/mdns/dns_sd_test.go) | Go | 62 | 0 | 7 | 69 |
| [volts/internal/mdns/server.go](/volts/internal/mdns/server.go) | Go | 352 | 93 | 73 | 518 |
| [volts/internal/mdns/server_test.go](/volts/internal/mdns/server_test.go) | Go | 59 | 0 | 7 | 66 |
| [volts/internal/mdns/zone.go](/volts/internal/mdns/zone.go) | Go | 241 | 39 | 30 | 310 |
| [volts/internal/mdns/zone_test.go](/volts/internal/mdns/zone_test.go) | Go | 255 | 0 | 21 | 276 |
| [volts/internal/metadata/metadata.go](/volts/internal/metadata/metadata.go) | Go | 89 | 19 | 19 | 127 |
| [volts/internal/metadata/metadata_test.go](/volts/internal/metadata/metadata_test.go) | Go | 98 | 0 | 18 | 116 |
| [volts/internal/net/net.go](/volts/internal/net/net.go) | Go | 79 | 19 | 23 | 121 |
| [volts/internal/pool/config.go](/volts/internal/pool/config.go) | Go | 26 | 0 | 8 | 34 |
| [volts/internal/pool/default.go](/volts/internal/pool/default.go) | Go | 86 | 8 | 20 | 114 |
| [volts/internal/pool/default_test.go](/volts/internal/pool/default_test.go) | Go | 11 | 73 | 4 | 88 |
| [volts/internal/pool/pool.go](/volts/internal/pool/pool.go) | Go | 22 | 8 | 6 | 36 |
| [volts/internal/registry/registry.go](/volts/internal/registry/registry.go) | Go | 111 | 16 | 22 | 149 |
| [volts/internal/test/api.go](/volts/internal/test/api.go) | Go | 32 | 3 | 8 | 43 |
| [volts/internal/test/base/unsafe_OutputAnyParameter_test.go](/volts/internal/test/base/unsafe_OutputAnyParameter_test.go) | Go | 65 | 12 | 16 | 93 |
| [volts/internal/test/client/rpc_client_test.go](/volts/internal/test/client/rpc_client_test.go) | Go | 51 | 1 | 13 | 65 |
| [volts/internal/test/controller.go](/volts/internal/test/controller.go) | Go | 47 | 3 | 12 | 62 |
| [volts/internal/test/data.go](/volts/internal/test/data.go) | Go | 14 | 3 | 4 | 21 |
| [volts/internal/test/middleware.go](/volts/internal/test/middleware.go) | Go | 17 | 0 | 7 | 24 |
| [volts/internal/test/router/force_export.go](/volts/internal/test/router/force_export.go) | Go | 85 | 32 | 16 | 133 |
| [volts/internal/test/router/handler_call_benchmark_test.go](/volts/internal/test/router/handler_call_benchmark_test.go) | Go | 51 | 0 | 10 | 61 |
| [volts/internal/test/router/handler_test.go](/volts/internal/test/router/handler_test.go) | Go | 98 | 46 | 28 | 172 |
| [volts/internal/test/router/reflect.go](/volts/internal/test/router/reflect.go) | Go | 314 | 210 | 47 | 571 |
| [volts/internal/test/router/struct.go](/volts/internal/test/router/struct.go) | Go | 41 | 0 | 7 | 48 |
| [volts/internal/test/server/http_server_test.go](/volts/internal/test/server/http_server_test.go) | Go | 89 | 3 | 19 | 111 |
| [volts/internal/test/test.go](/volts/internal/test/test.go) | Go | 6 | 0 | 4 | 10 |
| [volts/internal/time/time.go](/volts/internal/time/time.go) | Go | 54 | 0 | 8 | 62 |
| [volts/internal/tls/tls.go](/volts/internal/tls/tls.go) | Go | 60 | 2 | 13 | 75 |
| [volts/logger/config.go](/volts/logger/config.go) | Go | 68 | 5 | 14 | 87 |
| [volts/logger/console.go](/volts/logger/console.go) | Go | 24 | 20 | 9 | 53 |
| [volts/logger/level.go](/volts/logger/level.go) | Go | 77 | 1 | 8 | 86 |
| [volts/logger/logger.go](/volts/logger/logger.go) | Go | 301 | 82 | 68 | 451 |
| [volts/logger/logger_test.go](/volts/logger/logger_test.go) | Go | 33 | 0 | 8 | 41 |
| [volts/logger/writer.go](/volts/logger/writer.go) | Go | 89 | 11 | 17 | 117 |
| [volts/registry/README.md](/volts/registry/README.md) | Markdown | 1 | 0 | 0 | 1 |
| [volts/registry/cacher/cacher.go](/volts/registry/cacher/cacher.go) | Go | 380 | 78 | 96 | 554 |
| [volts/registry/config.go](/volts/registry/config.go) | Go | 121 | 14 | 28 | 163 |
| [volts/registry/consul/config.go](/volts/registry/consul/config.go) | Go | 53 | 21 | 8 | 82 |
| [volts/registry/consul/consul.go](/volts/registry/consul/consul.go) | Go | 324 | 46 | 79 | 449 |
| [volts/registry/consul/encoding.go](/volts/registry/consul/encoding.go) | Go | 125 | 13 | 34 | 172 |
| [volts/registry/consul/encoding_test.go](/volts/registry/consul/encoding_test.go) | Go | 118 | 9 | 21 | 148 |
| [volts/registry/consul/registry_test.go](/volts/registry/consul/registry_test.go) | Go | 177 | 4 | 28 | 209 |
| [volts/registry/consul/watcher.go](/volts/registry/consul/watcher.go) | Go | 213 | 34 | 42 | 289 |
| [volts/registry/consul/watcher_test.go](/volts/registry/consul/watcher_test.go) | Go | 72 | 0 | 15 | 87 |
| [volts/registry/etcd/config.go](/volts/registry/etcd/config.go) | Go | 28 | 2 | 8 | 38 |
| [volts/registry/etcd/etcd.go](/volts/registry/etcd/etcd.go) | Go | 322 | 27 | 73 | 422 |
| [volts/registry/etcd/watcher.go](/volts/registry/etcd/watcher.go) | Go | 79 | 1 | 16 | 96 |
| [volts/registry/mdns/config.go](/volts/registry/mdns/config.go) | Go | 2 | 1 | 2 | 5 |
| [volts/registry/mdns/mdns.go](/volts/registry/mdns/mdns.go) | Go | 478 | 49 | 98 | 625 |
| [volts/registry/mdns/mdns_test.go](/volts/registry/mdns/mdns_test.go) | Go | 277 | 9 | 58 | 344 |
| [volts/registry/memory/config.go](/volts/registry/memory/config.go) | Go | 13 | 1 | 4 | 18 |
| [volts/registry/memory/memory.go](/volts/registry/memory/memory.go) | Go | 241 | 4 | 51 | 296 |
| [volts/registry/memory/memory_test.go](/volts/registry/memory/memory_test.go) | Go | 213 | 5 | 29 | 247 |
| [volts/registry/memory/util.go](/volts/registry/memory/util.go) | Go | 103 | 1 | 19 | 123 |
| [volts/registry/memory/watcher.go](/volts/registry/memory/watcher.go) | Go | 32 | 0 | 6 | 38 |
| [volts/registry/nop.go](/volts/registry/nop.go) | Go | 39 | 7 | 12 | 58 |
| [volts/registry/registry.go](/volts/registry/registry.go) | Go | 112 | 18 | 23 | 153 |
| [volts/registry/watcher.go](/volts/registry/watcher.go) | Go | 36 | 15 | 8 | 59 |
| [volts/router/config.go](/volts/router/config.go) | Go | 201 | 14 | 37 | 252 |
| [volts/router/context.go](/volts/router/context.go) | Go | 40 | 3 | 8 | 51 |
| [volts/router/group.go](/volts/router/group.go) | Go | 327 | 146 | 69 | 542 |
| [volts/router/handler.go](/volts/router/handler.go) | Go | 397 | 149 | 66 | 612 |
| [volts/router/http.go](/volts/router/http.go) | Go | 488 | 124 | 90 | 702 |
| [volts/router/middleware.go](/volts/router/middleware.go) | Go | 57 | 7 | 16 | 80 |
| [volts/router/middleware/accept_language/accept_language.go](/volts/router/middleware/accept_language/accept_language.go) | Go | 47 | 3 | 10 | 60 |
| [volts/router/middleware/cors.go](/volts/router/middleware/cors.go) | Go | 21 | 8 | 6 | 35 |
| [volts/router/middleware/event/event.go](/volts/router/middleware/event/event.go) | Go | 39 | 0 | 9 | 48 |
| [volts/router/pool.go](/volts/router/pool.go) | Go | 59 | 8 | 14 | 81 |
| [volts/router/pprof.go](/volts/router/pprof.go) | Go | 25 | 1 | 6 | 32 |
| [volts/router/recover.go](/volts/router/recover.go) | Go | 18 | 1 | 3 | 22 |
| [volts/router/reverse_proxy.go](/volts/router/reverse_proxy.go) | Go | 97 | 10 | 27 | 134 |
| [volts/router/route.go](/volts/router/route.go) | Go | 102 | 25 | 20 | 147 |
| [volts/router/router.go](/volts/router/router.go) | Go | 389 | 169 | 78 | 636 |
| [volts/router/rpc.go](/volts/router/rpc.go) | Go | 182 | 6 | 41 | 229 |
| [volts/router/static.go](/volts/router/static.go) | Go | 32 | 4 | 11 | 47 |
| [volts/router/subscriber.go](/volts/router/subscriber.go) | Go | 220 | 21 | 37 | 278 |
| [volts/router/tree.go](/volts/router/tree.go) | Go | 602 | 87 | 107 | 796 |
| [volts/router/tree_test.go](/volts/router/tree_test.go) | Go | 26 | 0 | 6 | 32 |
| [volts/selector/config.go](/volts/selector/config.go) | Go | 88 | 10 | 17 | 115 |
| [volts/selector/filter.go](/volts/selector/filter.go) | Go | 67 | 9 | 16 | 92 |
| [volts/selector/registry.go](/volts/selector/registry.go) | Go | 81 | 7 | 21 | 109 |
| [volts/selector/selector.go](/volts/selector/selector.go) | Go | 42 | 12 | 11 | 65 |
| [volts/selector/strategy.go](/volts/selector/strategy.go) | Go | 55 | 2 | 17 | 74 |
| [volts/server/config.go](/volts/server/config.go) | Go | 249 | 35 | 47 | 331 |
| [volts/server/publisher.go](/volts/server/publisher.go) | Go | 103 | 15 | 23 | 141 |
| [volts/server/publisher_test.go](/volts/server/publisher_test.go) | Go | 4 | 0 | 4 | 8 |
| [volts/server/server.go](/volts/server/server.go) | Go | 368 | 103 | 95 | 566 |
| [volts/server/util.go](/volts/server/util.go) | Go | 23 | 4 | 6 | 33 |
| [volts/transport/README.md](/volts/transport/README.md) | Markdown | 2 | 0 | 0 | 2 |
| [volts/transport/compress.go](/volts/transport/compress.go) | Go | 35 | 2 | 5 | 42 |
| [volts/transport/config.go](/volts/transport/config.go) | Go | 228 | 42 | 47 | 317 |
| [volts/transport/http.go](/volts/transport/http.go) | Go | 220 | 39 | 41 | 300 |
| [volts/transport/http_client.go](/volts/transport/http_client.go) | Go | 120 | 4 | 29 | 153 |
| [volts/transport/http_proxy.go](/volts/transport/http_proxy.go) | Go | 320 | 20 | 51 | 391 |
| [volts/transport/http_request.go](/volts/transport/http_request.go) | Go | 65 | 9 | 17 | 91 |
| [volts/transport/http_resopnse.go](/volts/transport/http_resopnse.go) | Go | 100 | 9 | 22 | 131 |
| [volts/transport/http_server.go](/volts/transport/http_server.go) | Go | 62 | 5 | 13 | 80 |
| [volts/transport/http_sock.go](/volts/transport/http_sock.go) | Go | 172 | 35 | 48 | 255 |
| [volts/transport/listener.go](/volts/transport/listener.go) | Go | 96 | 12 | 25 | 133 |
| [volts/transport/message.go](/volts/transport/message.go) | Go | 280 | 97 | 68 | 445 |
| [volts/transport/message_test.go](/volts/transport/message_test.go) | Go | 10 | 0 | 4 | 14 |
| [volts/transport/pool.go](/volts/transport/pool.go) | Go | 69 | 8 | 14 | 91 |
| [volts/transport/sock.go](/volts/transport/sock.go) | Go | 82 | 19 | 16 | 117 |
| [volts/transport/tcp.go](/volts/transport/tcp.go) | Go | 104 | 5 | 25 | 134 |
| [volts/transport/tcp_client.go](/volts/transport/tcp_client.go) | Go | 13 | 0 | 4 | 17 |
| [volts/transport/tcp_request.go](/volts/transport/tcp_request.go) | Go | 80 | 9 | 22 | 111 |
| [volts/transport/tcp_response.go](/volts/transport/tcp_response.go) | Go | 56 | 3 | 12 | 71 |
| [volts/transport/tcp_server.go](/volts/transport/tcp_server.go) | Go | 81 | 13 | 21 | 115 |
| [volts/transport/tcp_sock.go](/volts/transport/tcp_sock.go) | Go | 48 | 12 | 16 | 76 |
| [volts/transport/tls.go](/volts/transport/tls.go) | Go | 224 | 53 | 21 | 298 |
| [volts/transport/transport.go](/volts/transport/transport.go) | Go | 63 | 9 | 15 | 87 |
| [volts/volts.go](/volts/volts.go) | Go | 92 | 35 | 27 | 154 |

[Summary](results.md) / Details / [Diff Summary](diff.md) / [Diff Details](diff-details.md)