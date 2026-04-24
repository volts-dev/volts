package router

import (
	"bytes"
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

// ------------------------------------------------------------
// TStaticStore — 实现 fs.FS，作为静态文件缓存层
// ------------------------------------------------------------

// TStaticStore 是静态文件的内存 TTL 缓存。
// 读取优先级：cache 命中 → 磁盘（diskDir）→ embed.FS。
// fsnotify 监听 diskDir，文件变动时失效对应 cache entry。
type TStaticStore struct {
	cache     *memory.TMemoryCache
	ttl       time.Duration
	diskDir   string // 磁盘目录（可为空）
	diskPath  string // 磁盘路径
	embedFS   fs.FS  // embed 来源（可为 nil）
	stopOnce  sync.Once
	watchOnce sync.Once
	stop      chan struct{}
}

func newStaticStore(ttl time.Duration, diskPath string, embedFS fs.FS) *TStaticStore {
	expireSec := int(ttl.Seconds())
	intervalSec := expireSec
	if expireSec <= 0 {
		expireSec = -1 // cacher: -1 = never expire (vacuum skips TTL<=0)
		intervalSec = 60
	}

	return &TStaticStore{
		cache:    memory.New(memory.WithExpire(expireSec), memory.WithInterval(intervalSec)),
		ttl:      ttl,
		diskPath: diskPath,
		diskDir:  filepath.Base(diskPath),
		embedFS:  embedFS,
		stop:     make(chan struct{}),
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
			if data, ok := val.([]byte); ok {
				return newMemFile(name, data), nil
			}
		}
	}

	// 2. 读磁盘（优先）
	if s.diskPath != "" {
		diskPath := filepath.Join(s.diskPath, filepath.FromSlash(name))
		if data, err := os.ReadFile(diskPath); err == nil {
			s.setCache(name, data)
			return newMemFile(name, data), nil
		}
	}

	// 3. 读 embed.FS
	if s.embedFS != nil {
		if f, err := s.embedFS.Open(name); err == nil {
			data, err := io.ReadAll(f)
			if err != nil {
				f.Close()
				return nil, fs.ErrNotExist
			}
			f.Close()
			s.setCache(name, data)
			return newMemFile(name, data), nil
		}
	}

	return nil, fs.ErrNotExist
}

func (s *TStaticStore) setCache(name string, data []byte) {
	blockTTL := s.ttl
	if blockTTL <= 0 {
		blockTTL = -1 // cacher: -1 = never expire for this entry
	}
	s.cache.Set(&cacher.CacheBlock{
		Key:        name,
		Value:      data,
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

func newMemFile(name string, data []byte) *memFile {
	return &memFile{
		Reader: bytes.NewReader(data),
		info:   &memFileInfo{name: path.Base(name), size: int64(len(data))},
	}
}

func (f *memFile) Close() error               { return nil }
func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }

type memFileInfo struct {
	name string
	size int64
}

func (i *memFileInfo) Name() string       { return i.name }
func (i *memFileInfo) Size() int64        { return i.size }
func (i *memFileInfo) Mode() fs.FileMode  { return 0444 }
func (i *memFileInfo) ModTime() time.Time { return time.Time{} }
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

// staticStoreHandler 基于 TStaticStore 的静态文件 handler
func staticStoreHandler(urlPattern string, store *TStaticStore) func(c *THttpContext) {
	fileServer := http.StripPrefix(urlPattern, http.FileServer(http.FS(store)))
	return func(ctx *THttpContext) {
		defer ctx.Apply()
		fileServer.ServeHTTP(ctx.response, ctx.request.Request)
	}
}

// rootStaticHandler 支持服务器 root 文件夹下的文件
func rootStaticHandler(ctx *THttpContext) {
	defer ctx.Apply()

	p := ctx.PathParams()
	fileExt := strings.ToLower(p.FieldByName("ext").AsString())

	if utils.IndexOf(fileExt, "", "html", "txt", "xml") == -1 {
		ctx.NotFound()
	}

	filePath := filepath.Clean(ctx.request.URL.Path[1:])
	if strings.Contains(filePath, "..") {
		ctx.response.WriteHeader(http.StatusForbidden)
		return
	}

	ctx.ServeFile(filePath)
}
