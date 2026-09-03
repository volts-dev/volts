package outbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/transport"
)

// 同一条线路（= 一个身份 = 指纹 + UA + 出口 IP）上同时在飞多个请求，是最容易招来
// 拒绝的形态之一：h1 会为第二个请求另拨一条 TCP 连接，h2 会在同一条连接上开第二条
// 流 —— 对方看到的都是"同一个浏览器、同一个 IP，在同一毫秒里发出了几个互不相关的
// 请求"。这一组用例守的就是这件事不会发生。

// concurrencyProbe 起一个记录**并发峰值**的服务。
//
// 每个 handler 进来先加一，停 hold 让重叠有机会发生，再减一。峰值靠取样得到：
// 不停一下的话本机的 httptest 快到几乎不可能真的重叠，于是一个坏掉的闸门也能
// 测出峰值 1 —— 那样这个用例只是在自我肯定。
func concurrencyProbe(t *testing.T, hold time.Duration) (*httptest.Server, func() int) {
	t.Helper()
	var (
		mu       sync.Mutex
		now, max int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		now++
		if now > max {
			max = now
		}
		mu.Unlock()

		time.Sleep(hold)

		mu.Lock()
		now--
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return max
	}
}

// fire 并发打 n 个请求，每个都把 body 读完关掉（位置就是在关闭时放开的）。
func fire(t *testing.T, r *rotator, url string, n int) {
	t.Helper()
	cli := &http.Client{Transport: r}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rsp, err := cli.Get(url)
			if err != nil {
				t.Errorf("请求失败: %v", err)
				return
			}
			io.Copy(io.Discard, rsp.Body)
			rsp.Body.Close()
		}()
	}
	wg.Wait()
}

// TestSameIdentityRequestsAreSerialized —— 一个身份上一次只准有一个请求在飞。
func TestSameIdentityRequestsAreSerialized(t *testing.T) {
	srv, peak := concurrencyProbe(t, 20*time.Millisecond)
	r := testRotatorN(1)

	fire(t, r, srv.URL, 6)

	if got := peak(); got != 1 {
		t.Fatalf("同一个身份上的并发峰值 = %d，想要 1 —— 闸门没起作用，"+
			"对方看到的是一个浏览器在同一毫秒里发了 %d 个请求", got, got)
	}
}

// TestDifferentIdentitiesStillRunInParallel —— 闸门关的是"同一条线路上的并行"，
// 不是并行本身。
//
// 这条与上一条一样重要：把并发一刀切掉的话，多配几个代理出口就没有意义了，
// 而"加一个出口"正是本包给出的加并发的正确做法。
func TestDifferentIdentitiesStillRunInParallel(t *testing.T) {
	const n = 3
	srv, peak := concurrencyProbe(t, 60*time.Millisecond)

	// 三个出口 ⇒ 三条线路 ⇒ 三个常备身份（见 assemble）。
	fps := []*transport.Fingerprint{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	active, _ := assemble(fps, []*proxyEndpoint{nil, nil, nil}, false, 1)

	fire(t, testRotator(active...), srv.URL, n)

	if got := peak(); got != n {
		t.Fatalf("%d 条线路上的并发峰值 = %d，想要 %d —— 闸门把不同身份之间的"+
			"并行也一起关掉了", n, got, n)
	}
}

// TestSlotIsHeldUntilBodyIsClosed —— 位置留到响应体关闭才放。
//
// 拿到响应头就放的话，那条连接还在传 body，下一个请求会另拨一条连接出去 ——
// 对方看到的仍然是同一个身份上两个重叠的请求，闸门形同虚设。
func TestSlotIsHeldUntilBodyIsClosed(t *testing.T) {
	srv, _ := concurrencyProbe(t, 0)
	r := testRotatorN(1)

	rsp, err := (&http.Client{Transport: r}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	// 响应头已经回来了，但 body 还没关 —— 这条线路仍然算占着。
	if p, done := r.take(time.Now()); p != nil {
		done()
		t.Fatal("响应体还没关就把位置放了 —— 下一个请求会与它重叠")
	}
	if got := r.personas[0].status(time.Now()); got != "A —— 使用中" {
		t.Errorf("Status 里写着 %q，看不出这条线路正被占着", got)
	}

	io.Copy(io.Discard, rsp.Body)
	rsp.Body.Close()

	p, done := r.take(time.Now())
	if p == nil {
		t.Fatal("响应体关掉之后位置仍然没放开")
	}
	done()
}

// TestForgottenBodyCloseDoesNotWedgeTheIdentity —— 忘了 Close 只该泄漏，不该停摆。
//
// 原本忘关 Body 的代价是泄漏一条连接；把位置绑到 Close 上之后，同一个疏忽会把
// 整条线路永久堵死 —— 在只有一个身份的部署里就是整个出站永久堵死。不能让本包
// 把一个泄漏升级成一次停摆，所以有 maxSlotHold 这道泄漏阀。
func TestForgottenBodyCloseDoesNotWedgeTheIdentity(t *testing.T) {
	old := maxSlotHold
	maxSlotHold = 30 * time.Millisecond
	t.Cleanup(func() { maxSlotHold = old })

	srv, _ := concurrencyProbe(t, 0)
	r := testRotatorN(1)

	if _, err := (&http.Client{Transport: r}).Get(srv.URL); err != nil { // 故意不关 body
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if p, done := r.take(time.Now()); p != nil {
			done()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("调用方忘了关 body，这条线路就再也没人能用了")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSlotIsReleasedWhenBlockedOrFailed —— 被挡回来 / 网络出错都要把位置放掉。
//
// 漏放一次就少一条线路，而症状是"后来的请求越来越慢"，与被限速长得一模一样。
func TestSlotIsReleasedWhenBlockedOrFailed(t *testing.T) {
	t.Run("被挡回 403", func(t *testing.T) {
		srv, _ := counting(t, func(int32) int { return http.StatusForbidden })
		r := testRotatorN(1)

		rsp, err := (&http.Client{Transport: r}).Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		rsp.Body.Close()

		if n := len(r.personas[0].sem); n != 0 {
			t.Fatalf("被挡之后位置没放开（闸门里还占着 %d 个）", n)
		}
	})

	t.Run("连不上", func(t *testing.T) {
		r := testRotatorN(1)
		if _, err := (&http.Client{Transport: r}).Get("http://127.0.0.1:1/"); err == nil {
			t.Fatal("连一个没人听的端口居然成功了")
		}
		if n := len(r.personas[0].sem); n != 0 {
			t.Fatalf("失败之后位置没放开（闸门里还占着 %d 个）", n)
		}
	})
}

// TestBusyIdentityIsSkippedWhileAnotherIsFree —— 忙的跳过去，不必排队。
func TestBusyIdentityIsSkippedWhileAnotherIsFree(t *testing.T) {
	r := testRotatorN(2)
	if !r.personas[0].tryAcquire() {
		t.Fatal("占不住 A")
	}

	p, done := r.take(time.Now())
	if p != r.personas[1] {
		t.Fatalf("取到 %v，想要 B —— A 正忙着", p)
	}
	done()
}

// TestBusyActivesDoNotPromoteReserves —— 备用是"常备被挡住了"的例外，不是"常备
// 正忙"的例外。
//
// 为了一时的忙就把备用拉进来，稳态立刻退回成"同一个 IP 上轮换多个指纹"——
// 而那正是这个包论证过的、比不轮换更可疑的做法。
func TestBusyActivesDoNotPromoteReserves(t *testing.T) {
	r := testRotatorN(1)
	r.reserves = []*persona{labelled("R0")}

	if !r.personas[0].tryAcquire() {
		t.Fatal("占不住常备身份")
	}
	if p, done := r.take(time.Now()); p != nil {
		done()
		t.Fatalf("常备只是忙，就动用了 %s", p.label)
	}

	// 换成"被挡回来"，备用才该顶上。
	r.personas[0].markBlocked(time.Minute, 0)
	p, done := r.take(time.Now())
	if p != r.reserves[0] {
		t.Fatalf("常备被挡之后取到 %v，想要 R0", p)
	}
	done()
}

// TestWaitingForASlotSaysWhatItWasWaitingFor —— 等不到位置时的错误必须点名。
//
// 只回一句 context deadline exceeded 的话，"我们自己排队排超时了"与"对方不回话"
// 分不开 —— 而这两者要做的事完全相反。
func TestWaitingForASlotSaysWhatItWasWaitingFor(t *testing.T) {
	r := testRotatorN(1)
	if !r.personas[0].tryAcquire() {
		t.Fatal("占不住 A")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	p, done, err := r.acquire(ctx)
	if err == nil {
		done()
		t.Fatalf("身份全占着还取到了 %s", p.label)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("没有把 ctx 的原因包进去: %v", err)
	}
	for _, want := range []string{"出站身份", "串行"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误里没有 %q，看不出是在等我们自己的闸门: %v", want, err)
		}
	}
}

// TestMaxPerIdentityRaisesTheGate —— 闸门容量可配。
func TestMaxPerIdentityRaisesTheGate(t *testing.T) {
	if got := (&Config{}).slots(); got != 1 {
		t.Fatalf("没配时闸门容量 = %d，想要 1", got)
	}
	if got := (&Config{MaxPerIdentity: 3}).slots(); got != 3 {
		t.Fatalf("配了 3 却是 %d", got)
	}

	srv, peak := concurrencyProbe(t, 60*time.Millisecond)
	fps := []*transport.Fingerprint{{Name: "a"}}
	active, _ := assemble(fps, []*proxyEndpoint{nil}, false, 2)

	fire(t, testRotator(active...), srv.URL, 4)

	if got := peak(); got != 2 {
		t.Fatalf("max_per_identity=2 时并发峰值 = %d，想要 2", got)
	}
}

// TestReleaserOnlyReleasesItsOwnSlot —— 释放函数必须是"每次占位一个"。
//
// 做成 p.release() 那样的方法的话，闸门容量大于 1 时一个多放了一次的调用方
// 会把别人的位置也放掉，于是并发悄悄变成 N+1 —— 没有任何报错。
func TestReleaserOnlyReleasesItsOwnSlot(t *testing.T) {
	r := testRotator(newPersona(nil, 0, nil, 2))
	p := r.personas[0]

	_, d1 := r.take(time.Now())
	_, d2 := r.take(time.Now())
	if d1 == nil || d2 == nil {
		t.Fatal("容量 2 的闸门没能连着占住两次")
	}

	d1()
	d1() // 重复释放（slotBody.Close 与 maxSlotHold 会一起触发，必须无害）
	d1()
	if n := len(p.sem); n != 1 {
		t.Fatalf("重复释放把别人的位置也放掉了：闸门里剩 %d 个，想要 1", n)
	}
	d2()
	if n := len(p.sem); n != 0 {
		t.Fatalf("全部释放后闸门里还剩 %d 个", n)
	}
}

func contains(s, sub string) bool     { return strings.Contains(s, sub) }
func errorsIs(err, target error) bool { return errors.Is(err, target) }
