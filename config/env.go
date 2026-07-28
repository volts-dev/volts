package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
)

// 环境变量引用。
//
// 配置文件里可以写 ${VAR} 把值外置到环境变量：
//
//	{"db": {"db_password": "${VS_DB_PASSWORD}"}}
//	{"db": {"db_master_host": "${VS_DB_HOST:-localhost:5432}"}}
//
// 动机是实测出来的：同一个 auth_secret_key 明文躺在 9 份 config.json 里
// （kylin、各 cmd/、各模块微服务各一份），改一次要同步九处，而配置文件通常随代码
// 分发、进备份、进版本库。
//
// 三条规则：
//
//   - `${VAR}` 未设置时**报错**，不是静默留空。密钥留空会让服务用空密钥继续跑起来，
//     比起不来危险得多 —— 签名校验退化成人人可伪造。
//   - `${VAR:-默认值}` 是显式声明「可缺省」，此时用默认值，不报错。
//   - `$${...}` 转义成字面 `${...}`，给确实要写这两个字符的场合留出路。
//
// 不写 ${} 的值原样保留，所以现有配置文件一个字都不用改。

// expandEnvRefs 展开模型里所有字符串字段中的 ${VAR} 引用。
//
// 只走**字符串**（含 []string、map[string]string 和嵌套 struct 里的字符串）：
// 环境变量本身就是字符串，对数值字段做展开只会带来意外。
func expandEnvRefs(model IConfig) []error {
	rv := reflect.ValueOf(model)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil
	}
	var errs []error
	expandStruct(rv.Elem(), model.String(), &errs)
	return errs
}

func expandStruct(v reflect.Value, path string, errs *[]error) {
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// 匿名内嵌（config.Config 带着 core / model 指针）与不可导出字段一律不碰
		if f.Anonymous || f.PkgPath != "" {
			continue
		}
		tag := f.Tag.Get("field")
		if tag == "-" || strings.HasPrefix(tag, "-,") {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		expandValue(v.Field(i), path+"."+name, errs)
	}
}

func expandValue(v reflect.Value, path string, errs *[]error) {
	if !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		out, err := expandString(v.String())
		if err != nil {
			*errs = append(*errs, fmt.Errorf("%s: %w", path, err))
			return
		}
		v.SetString(out)

	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() != reflect.String {
			return
		}
		for i := 0; i < v.Len(); i++ {
			expandValue(v.Index(i), fmt.Sprintf("%s[%d]", path, i), errs)
		}

	case reflect.Map:
		if v.Type().Elem().Kind() != reflect.String {
			return
		}
		for _, k := range v.MapKeys() {
			out, err := expandString(v.MapIndex(k).String())
			if err != nil {
				*errs = append(*errs, fmt.Errorf("%s[%v]: %w", path, k.Interface(), err))
				continue
			}
			v.SetMapIndex(k, reflect.ValueOf(out))
		}

	case reflect.Struct:
		expandStruct(v, path, errs)

	case reflect.Ptr:
		if !v.IsNil() && v.Elem().Kind() == reflect.Struct {
			expandStruct(v.Elem(), path, errs)
		}
	}
}

// expandString 展开一个字符串里的全部 ${VAR} / ${VAR:-默认值} 引用。
func expandString(s string) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}

	var b strings.Builder
	for i := 0; i < len(s); {
		// $${ -> 字面 ${
		if s[i] == '$' && i+2 < len(s) && s[i+1] == '$' && s[i+2] == '{' {
			b.WriteString("${")
			i += 3
			continue
		}
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				// 没有闭合的 } —— 原样输出，交给使用者自己发现
				b.WriteString(s[i:])
				break
			}
			ref := s[i+2 : i+end]
			val, err := lookupEnvRef(ref)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i += end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), nil
}

func lookupEnvRef(ref string) (string, error) {
	name, def, hasDefault := strings.Cut(ref, ":-")
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("环境变量引用 ${%s} 的变量名为空", ref)
	}

	if v, ok := os.LookupEnv(name); ok {
		return v, nil
	}
	if hasDefault {
		return def, nil
	}
	// 刻意报错而非留空：密钥为空会让服务带着空密钥跑起来，比起不来危险得多。
	// 确实允许缺省的写 ${NAME:-默认值}。
	return "", fmt.Errorf("环境变量 %s 未设置（允许缺省请写 ${%s:-默认值}）", name, name)
}

// hasEnvRef 报告一个字符串是否含环境变量引用。
func hasEnvRef(s string) bool {
	i := strings.Index(s, "${")
	if i < 0 {
		return false
	}
	// $${ 是转义，不算引用
	return !(i > 0 && s[i-1] == '$')
}
