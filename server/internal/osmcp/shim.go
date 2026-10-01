// shim.go — mcp-shim 子命令：gh 的 stdio MCP 插件适配器。
// 行分隔 JSON-RPC on stdin/stdout（与 gh mcp.DialStdio 的帧约定一致），逐行
// 转发到 OpenOS 服务进程的 /mcp 端点，应答原样写回。gh spawn 本进程的
// wrapper（grf-home/plugins/openos）即
// `openos-server mcp-shim <endpoint> <token> <user>`，端点、令牌与用户名由
// 安装期烙进 wrapper 内容。
//
// main 在一切服务初始化之前分派本子命令（shim 路径不打开数据库/不监听端口）。
// OpenOS 服务进程可能晚于 gh 的首次握手启动（冷启动竞态）：initialize 请求在
// 连接被拒时重试数秒，其余请求直接透传错误。
package osmcp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// RunShim 执行 stdio→HTTP 转发，返回进程退出码。args 为 os.Args[1:]，
// 形如 [mcp-shim, endpoint, token, user]。
func RunShim(args []string) int {
	if len(args) < 4 || args[0] != "mcp-shim" {
		fmt.Fprintln(os.Stderr, "用法: openos-server mcp-shim <mcp-endpoint> <token> <username>")
		return 2
	}
	endpoint, token, user := args[1], args[2], args[3]
	client := &http.Client{Timeout: 120 * time.Second}
	return runShim(os.Stdin, os.Stdout, client, endpoint, token, user)
}

func runShim(in io.Reader, out io.Writer, client *http.Client, endpoint, token, user string) int {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	w := bufio.NewWriter(out)
	defer w.Flush()

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		resp, err := postRPC(client, endpoint, token, user, line)
		// 冷启动竞态：OpenOS 可能尚未监听，initialize 重试（gh 握手整体
		// 时限 15s，这里最多耗 10s）。
		for err != nil && isInitialize(line) && retryable(err) {
			time.Sleep(1 * time.Second)
			resp, err = postRPC(client, endpoint, token, user, line)
		}
		if err != nil {
			// 以 JSON-RPC 错误应答，gh 能把失败记进插件警告而不是挂死
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"OpenOS 服务不可达: %s"}}%s`,
				rpcIDOf(line), jsonEscape(err.Error()), "\n")
			_ = w.Flush()
			continue
		}
		body := strings.TrimSpace(resp)
		if body != "" {
			_, _ = w.WriteString(body)
			_ = w.WriteByte('\n')
			_ = w.Flush()
		}
	}
	return 0
}

func postRPC(client *http.Client, endpoint, token, user, line string) (string, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(line)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-OS-User", user)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 应答是单条 application/json（本服务端不发 SSE）
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// isInitialize 判断一行请求是否 initialize 握手（重试判定用）。
func isInitialize(line string) bool {
	return strings.Contains(line, `"initialize"`)
}

// retryable 连接类错误（服务未起/端口未开）才值得重试。
func retryable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connect: connection") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe")
}

// rpcIDOf 从请求行里抠出 id（缺省 0；错误应答只需可解析）。
func rpcIDOf(line string) string {
	if i := strings.Index(line, `"id":`); i >= 0 {
		rest := line[i+5:]
		if j := strings.IndexAny(rest, ",}"); j > 0 {
			return rest[:j]
		}
	}
	return "0"
}

// jsonEscape 最小 JSON 字符串转义（错误消息进 JSON 用）。
func jsonEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
