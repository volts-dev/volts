package router

import (
	"fmt"
	"io"
	"slices"
	"testing"
	"time"

	coremem "github.com/volts-dev/volts/core/memory"
)

// 内存吃紧时静态文件缓存让路（core/memory 回收），丢掉的文件下次照样从磁盘读回来。
func TestStaticStore_ReclaimUnderMemoryPressure(t *testing.T) {
	dir := t.TempDir()
	store := newStaticStore(time.Minute, dir, nil)
	t.Cleanup(store.Stop)
	registerStaticStore(store)
	t.Cleanup(func() {
		staticStoresMu.Lock()
		allStaticStores = slices.DeleteFunc(allStaticStores, func(s *TStaticStore) bool { return s == store })
		staticStoresMu.Unlock()
	})

	for i := 0; i < 8; i++ {
		writeFile(t, dir, fmt.Sprintf("f%d.js", i), 64)
		f, err := store.Open(fmt.Sprintf("f%d.js", i))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if store.cache.Len() != 8 {
		t.Fatalf("缓存了 %d 个文件，want 8", store.cache.Len())
	}

	var found bool
	for _, c := range coremem.Current().Caches {
		if c.Name == "router.static" && c.Usage.Entries >= 8 {
			found = true
		}
	}
	if !found {
		t.Fatalf("router.static 没登记进 core/memory：%+v", coremem.Current().Caches)
	}

	if freed := reclaimStaticCaches(coremem.Trim); freed.Entries < 2 || store.cache.Len() != 6 {
		t.Fatalf("trim 应丢四分之一：freed %d, left %d", freed.Entries, store.cache.Len())
	}
	reclaimStaticCaches(coremem.Shed)
	if store.cache.Len() != 0 {
		t.Fatalf("shed 应整份清掉，还剩 %d", store.cache.Len())
	}

	f, err := store.Open("f3.js")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if b, _ := io.ReadAll(f); len(b) != 64 {
		t.Fatalf("清掉之后重读得到 %d 字节", len(b))
	}
}
