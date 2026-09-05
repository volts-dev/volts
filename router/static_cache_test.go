package router

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte("v"), size), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStaticStore_ModTimeIsReal 锁定内存文件带真实修改时间。
//
// 返回零值时 `http.ServeContent` 既不发 Last-Modified，也不处理 If-Modified-Since ——
// 静态资源完全没有协商缓存，浏览器缓存一过期就把整份内容重下一遍。
func TestStaticStore_ModTimeIsReal(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "app.js", 32)
	want, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	store := newStaticStore(time.Minute, dir, nil)
	t.Cleanup(store.Stop)

	f, err := store.Open("app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().IsZero() {
		t.Fatal("修改时间是零值 —— ServeContent 不会发 Last-Modified，也就永远回不了 304")
	}
	if !fi.ModTime().Equal(want.ModTime()) {
		t.Errorf("修改时间 %v，应为磁盘上的 %v", fi.ModTime(), want.ModTime())
	}

	// 第二次走缓存命中那一路：修改时间必须一起被缓存下来，否则表现成
	// "第一次能回 304、之后反而不能"，比全都不能更难查。
	f2, err := store.Open("app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	fi2, _ := f2.Stat()
	if fi2.ModTime().IsZero() || !fi2.ModTime().Equal(want.ModTime()) {
		t.Errorf("缓存命中时修改时间丢了：%v", fi2.ModTime())
	}
}

// TestStaticStore_ConditionalRequestGets304 是真栈验收：带 If-Modified-Since 的第二次
// 请求必须拿到 304 且 body 为空。
//
// 这是省下的真金白银 —— vertex 的主包压缩后 589KB，浏览器缓存过期后每次都整份重下，
// 而它其实一个字节都没变。
func TestStaticStore_ConditionalRequestGets304(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.js", 4096)

	store := newStaticStore(time.Minute, dir, nil)
	t.Cleanup(store.Stop)
	srv := httptest.NewServer(http.StripPrefix("/static/", http.FileServer(http.FS(store))))
	defer srv.Close()

	first, err := http.Get(srv.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != 200 || len(body) != 4096 {
		t.Fatalf("第一次请求异常: status=%d len=%d", first.StatusCode, len(body))
	}
	lastMod := first.Header.Get("Last-Modified")
	if lastMod == "" {
		t.Fatal("没有 Last-Modified，条件请求无从谈起")
	}

	req, _ := http.NewRequest("GET", srv.URL+"/static/app.js", nil)
	req.Header.Set("If-Modified-Since", lastMod)
	second, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	b2, _ := io.ReadAll(second.Body)

	if second.StatusCode != http.StatusNotModified {
		t.Fatalf("条件请求应回 304，实际 %d（内容没变却又整份重下了一次）", second.StatusCode)
	}
	if len(b2) != 0 {
		t.Errorf("304 不该带正文，实际 %d 字节", len(b2))
	}
}

// TestStaticStore_LargeFileNotCached 锁定大文件不进内存缓存。
//
// 缓存此前没有任何大小上限，而 StaticCacheTTL 配 0 就是永不过期 —— 静态目录里放一个
// 几十 MB 的文件，一次请求就把它常驻内存了。
func TestStaticStore_LargeFileNotCached(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "small.js", 1024)
	writeFile(t, dir, "big.bin", 4096)

	store := newStaticStore(time.Minute, dir, nil)
	t.Cleanup(store.Stop)
	store.maxFileSize = 2048 // 小文件进缓存，大文件不进

	for _, name := range []string{"small.js", "big.bin"} {
		f, err := store.Open(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f.Close()
	}

	if !store.cache.Exists("small.js") {
		t.Error("上限以内的文件应进缓存")
	}
	if store.cache.Exists("big.bin") {
		t.Error("超过上限的文件不该进缓存 —— 静态目录里放个大文件就能把它常驻内存")
	}
}

// TestStaticStore_LargeFileStillServesCorrectly 锁定不缓存不等于不能用：内容要完整，
// 而且仍然可 Seek（http.ServeContent 靠它做 Range 与内容嗅探）。
func TestStaticStore_LargeFileStillServesCorrectly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "big.bin", 8192)

	store := newStaticStore(time.Minute, dir, nil)
	t.Cleanup(store.Stop)
	store.maxFileSize = 1024

	f, err := store.Open("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, ok := f.(io.Seeker); !ok {
		t.Fatal("交出去的文件不可 Seek，ServeContent 无法做 Range 请求")
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 8192 {
		t.Errorf("内容不完整：%d 字节", len(data))
	}

	srv := httptest.NewServer(http.StripPrefix("/static/", http.FileServer(http.FS(store))))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/static/big.bin", nil)
	req.Header.Set("Range", "bytes=0-9")
	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rsp.Body.Close()
	part, _ := io.ReadAll(rsp.Body)
	if rsp.StatusCode != http.StatusPartialContent || len(part) != 10 {
		t.Errorf("Range 请求异常: status=%d len=%d", rsp.StatusCode, len(part))
	}
}

// TestStaticStore_RejectsTraversal 锁定路径穿越仍被挡住（改动没有削弱它）。
func TestStaticStore_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "static")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "secret.txt", 16)
	writeFile(t, sub, "ok.txt", 16)

	store := newStaticStore(time.Minute, sub, nil)
	t.Cleanup(store.Stop)

	for _, name := range []string{"../secret.txt", "..%2Fsecret.txt", "a/../../secret.txt"} {
		if f, err := store.Open(name); err == nil {
			f.Close()
			t.Errorf("穿越路径 %q 被放行了", name)
		}
	}
	if f, err := store.Open("ok.txt"); err != nil {
		t.Errorf("正常文件被误挡: %v", err)
	} else {
		f.Close()
	}
}

// TestStaticStore_NameWithDotDotIsRejectedOutright 记录一个刻意保留的取舍：
// 名字里含两个点的**普通文件**（`..hidden`）也一并被拒。
//
// `path.Clean("/"+name)` 之后已经不可能残留 `..` 元素，所以那句 `Contains(name, "..")`
// 是纯粹的第二道防线；代价是 `..hidden` 这类合法但罕见的文件名连带被挡。留着它 ——
// 为了一个几乎没人用的文件名去掉一道纵深防御，方向不对。
//
// 写下来是因为它会以"这个文件明明在目录里却 404"的形态出现，而目录列表里看得见它。
func TestStaticStore_NameWithDotDotIsRejectedOutright(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "..hidden", 8)

	store := newStaticStore(time.Minute, dir, nil)
	t.Cleanup(store.Stop)

	if f, err := store.Open("..hidden"); err == nil {
		f.Close()
		t.Error("行为变了：含 .. 的文件名现在被放行了。若是有意放宽，" +
			"请确认 symlink 那道闸的 rel 判据仍能挡住真正的越界（rel == \"..\" 或以 \"../\" 开头）")
	}
}

var _ fs.FS = (*TStaticStore)(nil)
