package router

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestStaticStore_EmbedOnly(t *testing.T) {
	fsys := fstest.MapFS{
		"hello.txt": &fstest.MapFile{Data: []byte("embed content")},
	}
	store := newStaticStore(60*time.Second, "", fsys)

	f, err := store.Open("hello.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "embed content" {
		t.Fatalf("got %q want %q", data, "embed content")
	}
}

func TestStaticStore_DiskOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "disk.txt"), []byte("disk content"), 0644); err != nil {
		t.Fatal(err)
	}
	store := newStaticStore(60*time.Second, dir, nil)

	f, err := store.Open("disk.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "disk content" {
		t.Fatalf("got %q want %q", data, "disk content")
	}
}

func TestStaticStore_DiskOverridesEmbed(t *testing.T) {
	fsys := fstest.MapFS{
		"file.txt": &fstest.MapFile{Data: []byte("embed version")},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("disk version"), 0644); err != nil {
		t.Fatal(err)
	}
	store := newStaticStore(60*time.Second, dir, fsys)

	f, err := store.Open("file.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "disk version" {
		t.Fatalf("disk should override embed: got %q", data)
	}
}

func TestStaticStore_CacheHit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cached.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	store := newStaticStore(60*time.Second, dir, nil)

	// 第一次读（写入 cache）
	f, err := store.Open("cached.txt")
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	io.ReadAll(f)
	f.Close()

	// 修改磁盘文件（不通过 watcher，cache 仍然有效）
	os.WriteFile(filepath.Join(dir, "cached.txt"), []byte("modified"), 0644)

	// 第二次读（应该命中 cache，返回旧内容）
	f2, err := store.Open("cached.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, _ := io.ReadAll(f2)
	f2.Close()
	if string(data) != "original" {
		t.Fatalf("cache hit should return original: got %q", data)
	}
}

func TestStaticStore_Miss(t *testing.T) {
	store := newStaticStore(60*time.Second, "", nil)
	_, err := store.Open("nonexistent.txt")
	if err != fs.ErrNotExist {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}
