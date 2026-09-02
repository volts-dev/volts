module github.com/volts-dev/volts/core/ja3

go 1.27

require (
	github.com/refraction-networking/utls v1.8.2
	github.com/volts-dev/volts v0.0.0
)

require (
	github.com/andybalholm/brotli v1.0.6 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-acme/lego/v4 v4.35.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.17.4 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/spf13/viper v1.21.0 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/volts-dev/dataset v0.0.0-20260707234140-fb059add5c90 // indirect
	github.com/volts-dev/utils v0.0.0-20260429065959-186108449dda // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// 开发期指回上级工作树；volts 发布后改为具体版本。
replace github.com/volts-dev/volts => ../..
