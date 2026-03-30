// demo/pubsub_web/main.go
//
// 完整的 Web 实时订阅 Demo：
//
//   架构：
//     [后台 goroutine]
//         每秒向 broker topic "metrics" 发布一条 JSON 消息
//     [broker subscriber]
//         收到消息后写入 Hub.Broadcast()
//     [SSE 端点 GET /events]
//         每个浏览器连接注册到 Hub，通过 text/event-stream 实时推送
//     [静态页面 GET /]
//         纯 HTML + JS，EventSource 连接 /events，动态更新表格
//
// 启动：go run ./demo/pubsub_web/
// 访问：http://localhost:8080
package pubsub_web

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/volts-dev/volts/broker"
	_ "github.com/volts-dev/volts/broker/memory"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

const (
	topic    = "metrics"
	httpAddr = ":8080"
)

var (
	log     = logger.New("pubsub-web")
	hub     = NewHub()
	eventID int64
)

// ---- broker message payload ------------------------------------------------

type MetricsPayload struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"mem"`
	QPS    int     `json:"qps"`
}

// metricsMsg 实现 server.IMessage
type metricsMsg struct {
	payload MetricsPayload
}

func (m *metricsMsg) Topic() string       { return topic }
func (m *metricsMsg) ContentType() string { return "application/json" }
func (m *metricsMsg) Payload() interface{} {
	return m.payload
}

// ---- subscriber handler ----------------------------------------------------

// onMetrics 由 broker 调用，将事件扇出到所有 SSE 客户端
func onMetrics(ctx *router.TSubscriberContext) {
	msg := ctx.Event.Message()
	id := atomic.AddInt64(&eventID, 1)

	evt := SseEvent{
		ID:    int(id),
		Topic: ctx.Event.Topic(),
		Body:  string(msg.Body),
		Time:  time.Now().Format("15:04:05.000"),
	}

	if hub.Count() > 0 {
		hub.Broadcast(evt)
	}
}

// ---- HTTP handlers ---------------------------------------------------------

// sseHandler 保持连接并持续推送 broker 消息给浏览器
func sseHandler(ctx *router.THttpContext) {
	rw := ctx.Response()
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	rw.Header().Set("Access-Control-Allow-Origin", "*")
	rw.WriteHeader(http.StatusOK)
	rw.Flush()

	// WriteStream 标记 isDone=true，阻止框架 Apply() 二次写入
	ctx.WriteStream(nil)

	clientID, ch, cancel := hub.Register()
	defer cancel()
	log.Infof("SSE client %d connected (total: %d)", clientID, hub.Count())

	reqCtx := ctx.Context()

	// 心跳，防止代理/浏览器断开空闲连接
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-reqCtx.Done():
			log.Infof("SSE client %d disconnected", clientID)
			return

		case <-ticker.C:
			fmt.Fprintf(rw, ": heartbeat\n\n")
			rw.Flush()

		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(rw, "id: %d\ndata: %s\n\n", evt.ID, data)
			rw.Flush()
		}
	}
}

// indexHandler 返回内嵌的 HTML 页面
func indexHandler(ctx *router.THttpContext) {
	ctx.SetHeader(true, "Content-Type", "text/html; charset=utf-8")
	ctx.Write([]byte(indexHTML))
}

// ---- embedded HTML ---------------------------------------------------------

const indexHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="UTF-8">
<title>Volts Pub/Sub 实时监控</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: "SF Pro Text", "Helvetica Neue", Arial, sans-serif;
         background: #0f1117; color: #e2e8f0; min-height: 100vh; padding: 24px; }
  h1   { font-size: 1.4rem; font-weight: 600; letter-spacing: .02em;
         color: #7dd3fc; margin-bottom: 4px; }
  .sub { font-size: .8rem; color: #64748b; margin-bottom: 24px; }
  .stats { display: flex; gap: 16px; margin-bottom: 24px; flex-wrap: wrap; }
  .card { flex: 1; min-width: 140px; background: #1e2535;
          border: 1px solid #2d3748; border-radius: 10px; padding: 16px; }
  .card .label { font-size: .7rem; color: #64748b; text-transform: uppercase;
                 letter-spacing: .08em; margin-bottom: 8px; }
  .card .value { font-size: 1.8rem; font-weight: 700; font-variant-numeric: tabular-nums; }
  #cpu-val  { color: #f59e0b; }
  #mem-val  { color: #34d399; }
  #qps-val  { color: #60a5fa; }
  #cnt-val  { color: #a78bfa; }
  .bar-wrap { background: #2d3748; border-radius: 4px; height: 6px;
              margin-top: 10px; overflow: hidden; }
  .bar      { height: 100%; border-radius: 4px;
              transition: width .5s ease; }
  #cpu-bar  { background: #f59e0b; }
  #mem-bar  { background: #34d399; }
  table { width: 100%; border-collapse: collapse; }
  thead th { text-align: left; font-size: .7rem; color: #64748b;
             text-transform: uppercase; letter-spacing: .08em;
             padding: 8px 12px; border-bottom: 1px solid #2d3748; }
  tbody tr { border-bottom: 1px solid #1e2535; transition: background .15s; }
  tbody tr:hover { background: #1e2535; }
  tbody td { padding: 8px 12px; font-size: .85rem;
             font-variant-numeric: tabular-nums; }
  td.ts { color: #64748b; font-size: .75rem; }
  .badge { display: inline-block; padding: 1px 7px; border-radius: 9px;
           font-size: .7rem; background: #1e3a5f; color: #7dd3fc; }
  .conn { display: inline-block; width: 8px; height: 8px; border-radius: 50%;
          background: #6b7280; margin-right: 6px; transition: background .3s; }
  .conn.live { background: #34d399; box-shadow: 0 0 6px #34d39988; }
  .footer { font-size: .75rem; color: #475569; margin-top: 16px; }
</style>
</head>
<body>
<h1>⚡ Volts Pub/Sub 实时监控</h1>
<p class="sub">
  <span class="conn" id="dot"></span>
  <span id="status">正在连接…</span>
</p>

<div class="stats">
  <div class="card">
    <div class="label">CPU</div>
    <div class="value" id="cpu-val">—</div>
    <div class="bar-wrap"><div class="bar" id="cpu-bar" style="width:0%"></div></div>
  </div>
  <div class="card">
    <div class="label">内存</div>
    <div class="value" id="mem-val">—</div>
    <div class="bar-wrap"><div class="bar" id="mem-bar" style="width:0%"></div></div>
  </div>
  <div class="card">
    <div class="label">QPS</div>
    <div class="value" id="qps-val">—</div>
  </div>
  <div class="card">
    <div class="label">消息总数</div>
    <div class="value" id="cnt-val">0</div>
  </div>
</div>

<table>
  <thead>
    <tr>
      <th>#</th><th>时间</th><th>Topic</th>
      <th>CPU %</th><th>内存 %</th><th>QPS</th>
    </tr>
  </thead>
  <tbody id="rows"></tbody>
</table>
<p class="footer">最近 50 条 · broker topic: <code>metrics</code></p>

<script>
const dot    = document.getElementById('dot');
const status = document.getElementById('status');
const rows   = document.getElementById('rows');
let   count  = 0;

const es = new EventSource('/events');

es.onopen = () => {
  dot.classList.add('live');
  status.textContent = '已连接，实时接收中…';
};

es.onerror = () => {
  dot.classList.remove('live');
  status.textContent = '连接断开，正在重连…';
};

es.onmessage = (e) => {
  const d = JSON.parse(e.data);
  let p = {};
  try { p = JSON.parse(d.body); } catch(_) {}

  count++;
  document.getElementById('cnt-val').textContent = count;

  const cpu = +(p.cpu || 0).toFixed(1);
  const mem = +(p.mem || 0).toFixed(1);
  const qps =  p.qps || 0;

  document.getElementById('cpu-val').textContent = cpu + '%';
  document.getElementById('mem-val').textContent = mem + '%';
  document.getElementById('qps-val').textContent = qps;
  document.getElementById('cpu-bar').style.width = cpu + '%';
  document.getElementById('mem-bar').style.width = mem + '%';

  const tr = document.createElement('tr');
  tr.innerHTML =
    '<td>' + d.id + '</td>' +
    '<td class="ts">' + d.time + '</td>' +
    '<td><span class="badge">' + d.topic + '</span></td>' +
    '<td>' + cpu + '%</td>' +
    '<td>' + mem + '%</td>' +
    '<td>' + qps + '</td>';

  rows.insertBefore(tr, rows.firstChild);

  // 保留最近 50 条
  while (rows.children.length > 50) {
    rows.removeChild(rows.lastChild);
  }
};
</script>
</body>
</html>`

// ---- entry point -----------------------------------------------------------

func Main() {
	memBroker := broker.Use("memory")

	// 订阅 broker topic
	r := router.New()
	if err := r.Subscribe(r.NewSubscriber(topic, onMetrics)); err != nil {
		log.Errf("subscribe: %v", err)
		os.Exit(1)
	}

	// HTTP 路由
	r.Url("GET", "/", indexHandler)
	r.Url("GET", "/events", sseHandler)

	// 启动 server
	srv := server.New(
		server.WithRouter(r),
		server.WithBroker(memBroker),
		server.WithTransport(transport.NewHTTPTransport(
			transport.WithAddrs(httpAddr),
		)),
	)
	if err := srv.Start(); err != nil {
		log.Errf("server start: %v", err)
		os.Exit(1)
	}
	defer srv.Stop()

	log.Infof("Listening on http://localhost%s", httpAddr)

	// 发布循环：每秒随机生成一条 metrics 消息
	pub := server.NewPublisher(server.WithPublisherBroker(memBroker))
	go func() {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		for {
			msg := &metricsMsg{
				payload: MetricsPayload{
					CPU:    rng.Float64() * 100,
					Memory: 30 + rng.Float64()*60,
					QPS:    rng.Intn(5000),
				},
			}
			if err := pub.Publish(context.Background(), msg); err != nil {
				log.Errf("publish: %v", err)
			}
			time.Sleep(time.Second)
		}
	}()

	// 等待退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down")
}
