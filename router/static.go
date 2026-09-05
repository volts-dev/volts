package router

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/volts-dev/cacher"
	"github.com/volts-dev/cacher/memory"
	"github.com/volts-dev/utils"
)

// ------------------------------------------------------------
// 包级静态 store 注册表（用于服务关闭时统一 Stop）
// ------------------------------------------------------------

var (
	staticStoresMu  sync.Mutex
	allStaticStores []*TStaticStore
)

func registerStaticStore(s *TStaticStore) {
	staticStoresMu.Lock()
	allStaticStores = append(allStaticStores, s)
	staticStoresMu.Unlock()
}

// StopAllStaticStores 停止所有 watcher goroutine。
// 由服务层在关闭时调用。
func StopAllStaticStores() {
	staticStoresMu.Lock()
	defer staticStoresMu.Unlock()
	for _, s := range allStaticStores {
		s.Stop()
	}
	allStaticStores = nil
}

// DefaultStaticCacheMaxFileSize 是单个文件进内存缓存的上限。
//
// 缓存此前没有任何大小限制：每个被请求过的文件都整份读进内存，按 TTL 留着，而
// StaticCacheTTL 配 0 就是**永不过期**。静态目录里放一个几十 MB 的视频或字体包，
// 一次请求就把它常驻了 —— 与 transport.Message.Decode 那次"16 字节报文换 67MB"是同
// 一类问题，只是这次的放大倍数由目录内容决定。超过上限的文件改为直接从磁盘/embed
// 流式返回，不进缓存：它们本来就摊不薄，缓存的收益远小于占住的内存。
const DefaultStaticCacheMaxFileSize = 4 << 20 // 4MiB

// staticEmbedModTime 是 embed 资源的修改时间基准。
//
// embed.FS 的文件没有修改时间，而 `http.ServeContent` 只在 modtime 非零时才发
// Last-Modified、才肯回 304 —— 于是 embed 静态资源从来没有过协商缓存，浏览器缓存一
// 过期就整份重下。可执行文件自己的 mtime 正是这批资源的"版本"：它们随二进制一起发布，
// 二进制不变则内容不变。取不到就退回进程启动时间（同一进程内稳定，重启后失效一次）。
var staticEmbedModTime = sync.OnceValue(func() time.Time {
	if exe, err := os.Executable(); err == nil {
		if fi, err := os.Stat(exe); err == nil {
			return fi.ModTime()
		}
	}
	return processStartTime
})

var processStartTime = time.Now()

// ------------------------------------------------------------
// TStaticStore — 实现 fs.FS，作为静态文件缓存层
// ------------------------------------------------------------

// TStaticStore 是静态文件的内存 TTL 缓存。
// 读取优先级：cache 命中 → 磁盘（diskDir）→ embed.FS。
// fsnotify 监听 diskDir，文件变动时失效对应 cache entry。
type TStaticStore struct {
	cache    *memory.TMemoryCache
	ttl      time.Duration
	diskDir  string // 磁盘目录（可为空）
	diskPath string // 磁盘路径
	embedFS  fs.FS  // embed 来源（可为 nil）
	// maxFileSize 单个文件进缓存的上限；<=0 用 DefaultStaticCacheMaxFileSize。
	maxFileSize int64
	stopOnce    sync.Once
	watchOnce   sync.Once
	stop        chan struct{}
}

func newStaticStore(ttl time.Duration, diskPath string, embedFS fs.FS) *TStaticStore {
	expireSec := int(ttl.Seconds())
	intervalSec := expireSec
	if expireSec <= 0 {
		expireSec = -1 // cacher: -1 = never expire (vacuum skips TTL<=0)
		intervalSec = 60
	}

	return &TStaticStore{
		cache:       memory.New(memory.WithExpire(expireSec), memory.WithInterval(intervalSec)),
		ttl:         ttl,
		diskPath:    diskPath,
		diskDir:     filepath.Base(diskPath),
		embedFS:     embedFS,
		maxFileSize: DefaultStaticCacheMaxFileSize,
		stop:        make(chan struct{}),
	}
}

// Open 实现 fs.FS。name 为相对路径，不含前导 '/'。
func (s *TStaticStore) Open(name string) (fs.File, error) {
	// 规范化路径，防止路径遍历
	name = path.Clean("/" + strings.TrimPrefix(name, "/"))[1:]
	if strings.Contains(name, "..") {
		return nil, fs.ErrNotExist
	}

	//fileName := filepath.Join(s.diskDir, filepath.FromSlash(name))

	// 1. 查 cache
	if s.cache.Exists(name) {
		if val, err := s.cache.Get(name); err == nil {
			if e, ok := val.(*staticEntry); ok {
				return newMemFile(name, e.data, e.modTime), nil
			}
		}
	}

	// 2. 读磁盘（优先）
	if s.diskPath != "" {
		diskPath := filepath.Join(s.diskPath, filepath.FromSlash(name))
		// symlink 防护：EvalSymlinks 解出真实路径，确保仍在 diskPath 之下。
		// 阻止恶意 / 误植的 symlink 指向 diskPath 之外的敏感文件。
		if real, err := filepath.EvalSymlinks(diskPath); err == nil {
			rootReal, rerr := filepath.EvalSymlinks(s.diskPath)
			if rerr != nil {
				return nil, fs.ErrNotExist
			}
			rel, rerr := filepath.Rel(rootReal, real)
			if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fs.ErrNotExist
			}
			if f, err := os.Open(real); err == nil {
				fi, serr := f.Stat()
				if serr != nil || fi.IsDir() {
					f.Close()
					return nil, fs.ErrNotExist
				}
				// 超过上限的不进缓存，直接把真实文件交出去：*os.File 本身就是
				// fs.File 且可 Seek，http.ServeContent 照常支持 Range 与 304。
				if fi.Size() > s.cacheLimit() {
					return f, nil
				}
				data, rerr := io.ReadAll(f)
				f.Close()
				if rerr != nil {
					return nil, fs.ErrNotExist
				}
				s.setCache(name, data, fi.ModTime())
				return newMemFile(name, data, fi.ModTime()), nil
			}
		}
	}

	// 3. 读 embed.FS
	if s.embedFS != nil {
		if f, err := s.embedFS.Open(name); err == nil {
			// embed 文件没有修改时间，用可执行文件的 mtime 代替（见
			// staticEmbedModTime）—— 没有它就永远发不出 Last-Modified，也就永远
			// 回不了 304。
			modTime := staticEmbedModTime()
			if fi, serr := f.Stat(); serr == nil && fi.Size() > s.cacheLimit() {
				return f, nil // 大文件不进缓存，embed 的 File 也可 Seek
			}
			data, rerr := io.ReadAll(f)
			f.Close()
			if rerr != nil {
				return nil, fs.ErrNotExist
			}
			s.setCache(name, data, modTime)
			return newMemFile(name, data, modTime), nil
		}
	}

	return nil, fs.ErrNotExist
}

// cacheLimit 返回单个文件进缓存的字节上限。
func (s *TStaticStore) cacheLimit() int64 {
	if s.maxFileSize > 0 {
		return s.maxFileSize
	}
	return DefaultStaticCacheMaxFileSize
}

// staticEntry 是缓存里存的东西：内容 + 它的修改时间。
//
// 修改时间必须一起缓存。只存 []byte 的话，命中缓存的那一路就交不出 modtime，
// `http.ServeContent` 于是不发 Last-Modified —— 表现为"第一次请求能回 304、后面反而
// 不能"，比全都不能更难查。
type staticEntry struct {
	data    []byte
	modTime time.Time
}

func (s *TStaticStore) setCache(name string, data []byte, modTime time.Time) {
	blockTTL := s.ttl
	if blockTTL <= 0 {
		blockTTL = -1 // cacher: -1 = never expire for this entry
	}
	s.cache.Set(&cacher.CacheBlock{
		Key:        name,
		Value:      &staticEntry{data: data, modTime: modTime},
		TTL:        blockTTL,
		LastAccess: time.Now(),
	})
}

// Invalidate 删除指定路径的 cache entry，由 watcher 回调调用。
func (s *TStaticStore) Invalidate(name string) {
	s.cache.Delete(name)
}

// Watch 启动 fsnotify 监听 diskPath。
// 文件创建/修改/删除时失效对应 cache entry。
// 需在 diskPath 非空时调用。多次调用安全（幂等）。
func (s *TStaticStore) Watch() error {
	if s.diskPath == "" {
		return nil // nothing to watch
	}
	var watchErr error
	s.watchOnce.Do(func() {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			watchErr = err
			return
		}
		// Walk diskDir and add all subdirectories to watcher.
		// NOTE: directories created after Watch() is called are not automatically watched.
		if err := filepath.WalkDir(s.diskPath, func(walkPath string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip inaccessible paths
			}
			if d.IsDir() {
				return watcher.Add(walkPath)
			}
			return nil
		}); err != nil {
			watcher.Close()
			watchErr = err
			return
		}
		go func() {
			defer watcher.Close()
			for {
				select {
				case event, ok := <-watcher.Events:
					if !ok {
						return
					}
					if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) || event.Has(fsnotify.Remove) {
						if rel, err := filepath.Rel(s.diskPath, event.Name); err == nil {
							s.Invalidate(filepath.ToSlash(rel))
						}
					}
				case <-watcher.Errors:
					// ignore watcher errors
				case <-s.stop:
					return
				}
			}
		}()
	})
	return watchErr
}

// Stop 关闭 watcher goroutine。可安全多次调用。
func (s *TStaticStore) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

// ------------------------------------------------------------
// memFile — fs.File 的内存实现，支持 io.ReadSeeker
// ------------------------------------------------------------

type memFile struct {
	*bytes.Reader
	info *memFileInfo
}

func newMemFile(name string, data []byte, modTime time.Time) *memFile {
	return &memFile{
		Reader: bytes.NewReader(data),
		info:   &memFileInfo{name: path.Base(name), size: int64(len(data)), modTime: modTime},
	}
}

func (f *memFile) Close() error               { return nil }
func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }

type memFileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (i *memFileInfo) Name() string      { return i.name }
func (i *memFileInfo) Size() int64       { return i.size }
func (i *memFileInfo) Mode() fs.FileMode { return 0444 }

// ModTime 必须是真实时间。返回零值时 `http.ServeContent` 既不发 Last-Modified，
// 也不处理 If-Modified-Since —— 静态资源于是完全没有协商缓存，浏览器缓存一过期就
// 整份重下。vertex 的主包 gzip 后 589KB，每小时重下一次，而它其实一个字节都没变。
func (i *memFileInfo) ModTime() time.Time { return i.modTime }
func (i *memFileInfo) IsDir() bool        { return false }
func (i *memFileInfo) Sys() any           { return nil }

// ------------------------------------------------------------
// staticHandler — 原有磁盘版（保留兼容）
// ------------------------------------------------------------

func staticHandler(urlPattern string, filePath string) func(c *THttpContext) {
	fs := http.Dir(filePath)
	fileServer := http.StripPrefix(urlPattern, http.FileServer(fs))

	return func(ctx *THttpContext) {
		defer ctx.Apply()

		file := filepath.Join(ctx.pathParams.FieldByName("filepath").AsString())
		cleanPath := filepath.Clean(file)
		if strings.Contains(cleanPath, "..") {
			ctx.response.WriteHeader(http.StatusForbidden)
			return
		}

		if _, err := fs.Open(file); err != nil {
			if ctx.handlerIndex == len(ctx.Route().Handlers())-1 {
				ctx.response.WriteHeader(http.StatusNotFound)
			}
			log.Warn(err)
			return
		}

		fileServer.ServeHTTP(ctx.response, ctx.request.Request)
	}
}

// browserStaticMaxAge 静态资源的浏览器缓存时长(秒)。此前不发任何缓存头,
// 每次页面访问都重复拉取全部 CSS/JS/图片(embed 资源无 mod time,连
// Last-Modified 协商缓存都没有)。资源随二进制发布,1 小时是保守值。
const browserStaticMaxAge = 3600

// staticStoreHandler 基于 TStaticStore 的静态文件 handler
func staticStoreHandler(urlPattern string, store *TStaticStore) func(c *THttpContext) {
	fileServer := http.StripPrefix(urlPattern, http.FileServer(http.FS(store)))
	return func(ctx *THttpContext) {
		defer ctx.Apply()
		if ctx.response.Header().Get("Cache-Control") == "" {
			ctx.response.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", browserStaticMaxAge))
		}
		fileServer.ServeHTTP(ctx.response, ctx.request.Request)
	}
}

// rootStaticHandler 支持服务器 root 文件夹下的文件
func rootStaticHandler(ctx *THttpContext) {
	defer ctx.Apply()

	p := ctx.PathParams()
	fileExt := strings.ToLower(p.FieldByName("ext").AsString())

	if utils.IndexOf(fileExt, "", "html", "txt", "xml") == -1 {
		// 必须 return：NotFound 只是写了 404 头和正文，不会终止本函数。此前漏了
		// 这一句，后面的 ServeFile 照样执行 —— 404 之后又把文件内容追加进同一条
		// 响应（net/http 记一条 superfluous WriteHeader），白名单形同虚设。
		ctx.NotFound()
		return
	}

	filePath := filepath.Clean(ctx.request.URL.Path[1:])
	if strings.Contains(filePath, "..") {
		ctx.response.WriteHeader(http.StatusForbidden)
		return
	}

	ctx.ServeFile(filePath)
}
