package router

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// TestRouter_DoesNotServeTheWorkingDirectory 是一条安全回归：**框架默认不把进程的
// 工作目录当文件服务器**。
//
// 曾经有个默认开着的 `UseRootStatics`，它注册 `GET /<:fileName>.<:ext>` 指向
// `rootStaticHandler`，而那个 handler 把 URL 直接当**工作目录下的相对路径**交给
// `http.ServeFile` —— 任何位于进程 CWD 的 .html / .txt / .xml 都会被发出去。
//
// 它一直没生效，纯属走运：路由树的占位符语法是 `{:name}`，而那行写的是 `<:name>`，
// 于是整条路径被当**字面量**登记，只匹配 `/<:fileName>.<:ext>` 这个字符串本身；
// 加上 `New()` 里 `config.Register` 触发 Load 时 `cfg.Router` 还是 nil，多数情况下
// 连那条字面路由都没建。整套（含 StaticDir / StaticExt 两个从没被读过的白名单字段）
// 已删除 —— 把语法"修对"等于给每个进程默认开一个工作目录文件服务器，而 vectors 那边
// /robots.txt、/favicon.ico 早就各自用显式路由做掉了。
//
// 这条测试钉的是**性质**不是实现：以后谁再想加根目录静态服务，必须先让它变红。
func TestRouter_DoesNotServeTheWorkingDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(cwd) })

	for _, name := range []string{"secret.xml", "notes.txt", "index.html", "app.json"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("CLASSIFIED"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r := New()
	defer r.Close()
	// 显式再走一次 Load：注册时机曾经是这条特性时有时无的原因，这里把两条路都覆盖。
	if err := r.Config().Load(); err != nil {
		t.Fatalf("Config().Load(): %v", err)
	}

	for _, p := range []string{"/secret.xml", "/notes.txt", "/index.html", "/app.json"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, transport.NewHttpRequest(httptest.NewRequest("GET", p, nil)))
		if rec.Code != 404 {
			t.Errorf("GET %s -> %d，应为 404", p, rec.Code)
		}
		if body := rec.Body.String(); body == "CLASSIFIED" {
			t.Errorf("GET %s 把工作目录里的文件发出去了", p)
		}
	}
}
