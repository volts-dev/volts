module github.com/volts-dev/volts

go 1.27

// 本地 cacher 已修 TMemoryCache.Push 的 Len() 锁外读竞态（发布版 v0314 仍带竞态，
// TestConcurrentPubBroker -race 可实锤）；发布 cacher 新版后移除本 replace 并 bump。
replace github.com/volts-dev/cacher => ../cacher

require (
	github.com/bwmarrin/snowflake v0.3.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/getkin/kin-openapi v0.142.0
	github.com/go-acme/lego/v4 v4.35.2
	github.com/go-viper/mapstructure/v2 v2.5.0
	github.com/google/uuid v1.6.0
	github.com/hashicorp/consul/api v1.34.4
	github.com/miekg/dns v1.1.72
	github.com/mitchellh/hashstructure/v2 v2.0.2
	github.com/spf13/viper v1.21.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
	github.com/volts-dev/cacher v0.0.0-20260314091036-7e3a317fca8b
	github.com/volts-dev/dataset v0.0.0-20260707234140-fb059add5c90
	github.com/volts-dev/template v0.0.0-20240613070915-3d2783a5c479
	github.com/volts-dev/utils v0.0.0-20260429065959-186108449dda
	go.etcd.io/etcd/api/v3 v3.7.0
	go.etcd.io/etcd/client/v3 v3.7.0
	go.uber.org/atomic v1.11.0
	go.uber.org/zap v1.28.0
	golang.org/x/crypto v0.54.0
	golang.org/x/net v0.57.0
	golang.org/x/sync v0.22.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/armon/go-metrics v0.4.1 // indirect
	github.com/coreos/go-semver v0.3.1 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-openapi/jsonpointer v0.22.5 // indirect
	github.com/go-openapi/swag/jsonname v0.25.5 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-metrics v0.6.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/golang-lru v1.0.2 // indirect
	github.com/hashicorp/serf v0.10.4 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/oasdiff/yaml v0.1.1 // indirect
	github.com/oasdiff/yaml3 v0.0.14 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go.etcd.io/etcd/client/pkg/v3 v3.7.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.81.0 // indirect
)
