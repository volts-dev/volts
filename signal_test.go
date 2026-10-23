package volts

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

type sigCtrl struct{}

func (sigCtrl) Ping(ctx *router.THttpContext) {}

// SIGTERM 要走优雅停服：Run 返回、注册记录被注销、进程正常退出。
//
// Config.Signal 曾从来没人置成 true：Run 不订阅信号，SIGTERM 让进程当场终止，停服流程（注销、
// 关传输层、BeforeStop/AfterStop）一次都没跑过，注册记录只能等租约过期。
func TestRun_SIGTERMStopsGracefully(t *testing.T) {
	if os.Getenv("VOLTS_SIGNAL_CHILD") == "1" {
		sigtermChild()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRun_SIGTERMStopsGracefully$", "-test.count=1")
	cmd.Env = append(os.Environ(), "VOLTS_SIGNAL_CHILD=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	var seen []string
	wait := func(marker string, d time.Duration) bool {
		deadline := time.After(d)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					return false
				}
				seen = append(seen, l)
				if strings.Contains(l, marker) {
					return true
				}
			case <-deadline:
				return false
			}
		}
	}

	if !wait("CHILD STARTED", 15*time.Second) {
		t.Fatalf("子进程没起来：\n%s", strings.Join(seen, "\n"))
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if !wait("CHILD RUN RETURNED", 15*time.Second) {
		t.Fatalf("收到 SIGTERM 后 Run 没有返回（进程被信号直接终止了？）：\n%s", strings.Join(seen, "\n"))
	}
	for range lines {
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("子进程没有正常退出：%v\n%s", err, strings.Join(seen, "\n"))
	}
	if !strings.Contains(strings.Join(seen, "\n"), "CHILD REGISTRY EMPTY") {
		t.Fatalf("停服后注册记录还在：\n%s", strings.Join(seen, "\n"))
	}
}

func sigtermChild() {
	reg := memory.New()
	r := router.New()
	g := router.NewGroup(router.WithAloneService(true, "sigsvc"))
	g.Url([]string{"GET"}, "/sigsvc/ping", sigCtrl.Ping)
	r.RegisterGroup(g)
	srv := server.New(server.Name("sigsrv"), server.Address("127.0.0.1:0"), server.WithRegistry(reg), server.WithRouter(r))

	app := New(Server(srv), AfterStart(func() error {
		if s, err := reg.GetService("sigsvc"); err != nil || len(s) == 0 {
			fmt.Println("CHILD NOT REGISTERED", err)
		}
		fmt.Println("CHILD STARTED")
		return nil
	}), AfterStop(func() error {
		if s, err := reg.GetService("sigsvc"); err != nil || len(s) == 0 || len(s[0].Nodes) == 0 {
			fmt.Println("CHILD REGISTRY EMPTY")
		}
		return nil
	}))
	_ = app.Run()
	fmt.Println("CHILD RUN RETURNED")
	os.Exit(0)
}

// 停服卡住时，到 StopTimeout 进程也必须退出：一次 SIGTERM 最终一定让进程走掉。
func TestRun_StopTimeoutExitsAHungShutdown(t *testing.T) {
	if os.Getenv("VOLTS_SIGNAL_CHILD") == "hang" {
		srv := server.New(server.Name("hangsrv"), server.Address("127.0.0.1:0"), server.WithRegistry(memory.New()))
		app := New(Server(srv), StopTimeout(300*time.Millisecond),
			AfterStart(func() error { fmt.Println("CHILD STARTED"); return nil }),
			BeforeStop(func() error { select {} }))
		_ = app.Run()
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRun_StopTimeoutExitsAHungShutdown$", "-test.count=1")
	cmd.Env = append(os.Environ(), "VOLTS_SIGNAL_CHILD=hang")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	started := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "CHILD STARTED") {
				close(started)
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("子进程没起来")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 1 {
			t.Fatalf("停服超时应以退出码 1 退出，得到 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("停服卡住时进程一直没退出")
	}
}
