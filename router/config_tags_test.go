package router

import (
	"reflect"
	"strings"
	"testing"

	"github.com/volts-dev/utils"
)

// TestConfig_PersistedFieldsCarryTags 锁定：Config 里每个要持久化的字段都带有显式的
// `field` 标签（要么点名，要么 "-"）。
//
// 读写两条路对没有标签的字段各有一套命名：读（mapstructure）按 SnakeCasedName 匹配，
// 认 selector_strategy；写（Struct2ItfMap）用 Go 字段名，写出 SelectorStrategy，
// viper 再压成 selectorstrategy。于是 SelectorStrategy / UsePprof / UseRootStatics
// 曾经是：文件里的 selector_strategy 读得进来，SaveToFile 却写出一个自己再也读不回
// 的键，而 Lint 把文件里正确的那个报成「未知配置项」。只要标签在，两条路用的就是
// 同一个名字。
func TestConfig_PersistedFieldsCarryTags(t *testing.T) {
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("field")
		if tag == "" {
			t.Errorf("Config.%s 没有 field 标签：写出的键是 %q，读回时却按 %q 匹配，永远对不上",
				f.Name, strings.ToLower(f.Name), utils.SnakeCasedName(f.Name))
		}
	}
}
