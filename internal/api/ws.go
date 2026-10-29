package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"warden/internal/httpx"
	"warden/internal/platform/docker"
	"warden/internal/store"
	"warden/internal/system"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  16 * 1024,
	WriteBufferSize: 16 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		// 仅允许同源，避免被其他站点嵌入
		host := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
		return strings.EqualFold(host, r.Host)
	},
}


// noticePrefix 用于向前端发送控制提示（非终端输出），前端会渲染成浮层。
const noticePrefix = "\x00NOTICE:"

func writeNotice(conn *websocket.Conn, msg string) {
	_ = conn.WriteMessage(websocket.TextMessage, []byte(noticePrefix+msg))
}

type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsConn) write(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (w *wsConn) writeText(s string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, []byte(s))
}

type wsWriter struct {
	w *wsConn
}

func (w wsWriter) Write(p []byte) (int, error) {
	if err := w.w.write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ---------- 系统指标推送 ----------

func (d *Deps) wsMetrics(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	w := &wsConn{conn: conn}

	// 客户端不再主动发消息，读循环仅用于感知断开
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				conn.Close()
				return
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	push := func() bool {
		snap, _ := d.Metrics.Snapshot()
		b, err := json.Marshal(gin.H{"type": "metrics", "data": snap})
		if err != nil {
			return false
		}
		return w.writeText(string(b)) == nil
	}
	if !push() {
		return
	}
	for range ticker.C {
		if !push() {
			return
		}
	}
}

// ---------- 容器日志实时流 ----------

func (d *Deps) wsContainerLogs(c *gin.Context) {
	id := c.Param("id")
	tail := c.DefaultQuery("tail", "200")
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	w := &wsConn{conn: conn}

	rc, tty, err := d.Docker.Logs(c.Request.Context(), id, docker.LogOptions{Tail: tail, Follow: true})
	if err != nil {
		_ = w.writeText(noticePrefix + "无法读取日志：" + err.Error())
		return
	}
	defer rc.Close()

	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				rc.Close()
				return
			}
		}
	}()

	writer := wsWriter{w: w}
	if tty {
		buf := make([]byte, 8192)
		for {
			n, err := rc.Read(buf)
			if n > 0 {
				if _, werr := writer.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	_, _ = stdcopy.StdCopy(writer, writer, rc)
}

// ---------- 通用终端桥接 ----------

func bridgeTerminal(conn *websocket.Conn, term system.Term) {
	w := &wsConn{conn: conn}
	done := make(chan struct{})

	go func() {
		defer close(done)
		buf := make([]byte, 16*1024)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				if werr := w.write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				_ = w.writeText("\r\n\x1b[33m[会话已结束]\x1b[0m\r\n")
				return
			}
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage {
			var ctrl struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
				_ = term.Resize(ctrl.Cols, ctrl.Rows)
				continue
			}
		}
		if _, err := term.Write(data); err != nil {
			break
		}
	}
	_ = term.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	_ = conn.Close()
}

func (d *Deps) wsLocalTerminal(c *gin.Context) {
	cols, _ := strconv.Atoi(c.DefaultQuery("cols", "100"))
	rows, _ := strconv.Atoi(c.DefaultQuery("rows", "30"))

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	term, err := system.StartLocalShell(d.Cfg.Shell)
	if err != nil {
		writeNotice(conn, err.Error())
		time.Sleep(300 * time.Millisecond)
		conn.Close()
		return
	}
	_ = term.Resize(uint16(cols), uint16(rows))
	bridgeTerminal(conn, term)
}

func (d *Deps) wsSSHTerminal(c *gin.Context) {
	id, _ := strconv.Atoi(c.Query("id"))
	var h store.SSHHost
	if err := d.Store.DB.First(&h, id).Error; err != nil {
		httpx.NotFound(c, "主机不存在")
		return
	}
	cols, _ := strconv.Atoi(c.DefaultQuery("cols", "100"))
	rows, _ := strconv.Atoi(c.DefaultQuery("rows", "30"))

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	target, err := d.targetFromHost(&h)
	if err != nil {
		writeNotice(conn, "凭据解密失败，请重新编辑该主机并保存密码")
		conn.Close()
		return
	}
	term, err := system.StartSSHShell(c.Request.Context(), target, uint16(cols), uint16(rows), "")
	if err != nil {
		writeNotice(conn, "SSH 连接失败："+err.Error())
		time.Sleep(300 * time.Millisecond)
		conn.Close()
		return
	}
	bridgeTerminal(conn, term)
}

// ---------- 容器交互终端 ----------

func (d *Deps) wsContainerExec(c *gin.Context) {
	id := c.Param("id")
	shell := c.DefaultQuery("shell", "/bin/sh")
	cols, _ := strconv.Atoi(c.DefaultQuery("cols", "100"))
	rows, _ := strconv.Atoi(c.DefaultQuery("rows", "30"))

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	sess, err := d.Docker.Exec(c.Request.Context(), id, uint(cols), uint(rows), shell)
	if err != nil {
		writeNotice(conn, "进入容器失败："+err.Error())
		time.Sleep(300 * time.Millisecond)
		conn.Close()
		return
	}
	defer sess.Close()

	w := &wsConn{conn: conn}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16*1024)
		for {
			n, err := sess.Reader.Read(buf)
			if n > 0 {
				if werr := w.write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage {
			var ctrl struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
				_ = sess.Resize(c.Request.Context(), uint(ctrl.Rows), uint(ctrl.Cols))
				continue
			}
		}
		if _, err := sess.Conn.Write(data); err != nil {
			break
		}
	}
	sess.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	conn.Close()
}
