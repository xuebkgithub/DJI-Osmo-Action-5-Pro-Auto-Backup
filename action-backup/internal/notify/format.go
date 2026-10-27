package notify

import (
	"fmt"
	"strings"
	"time"

	"action-backup/internal/events"
)

// 标题统一前缀，手机上一眼分辨来源。
const titlePrefix = "Action Backup"

func title(s string) string { return titlePrefix + " · " + s }

func formatTest(siteURL string) Message {
	return Message{Title: title("测试"), Body: "通知渠道已连通。" + time.Now().Format("2006-01-02 15:04:05"), URL: siteURL}
}

func formatDeviceAttached(e events.Event, siteURL string) Message {
	name := str(e.Data, "name")
	if name == "" {
		name = "相机"
	}
	b := fmt.Sprintf("%s 已插入", name)
	if sn := str(e.Data, "serial"); sn != "" {
		b += fmt.Sprintf("（SN %s）", sn)
	}
	if n := num(e.Data, "volumes"); n > 0 {
		b += fmt.Sprintf("，%d 个卷", n)
	}
	if boolean(e.Data, "weak_identity") {
		b += "；序列号无法识别"
	}
	return Message{Title: title("相机已插入"), Body: b, URL: siteURL}
}

func formatBackupStarted(e events.Event, siteURL string) Message {
	created, requeued := num(e.Data, "created"), num(e.Data, "requeued")
	parts := []string{fmt.Sprintf("新建 %d 个文件", created)}
	if requeued > 0 {
		parts = append(parts, fmt.Sprintf("重新入队 %d 个", requeued))
	}
	if b := num(e.Data, "upload_bytes"); b > 0 {
		parts = append(parts, "待上传 "+humanBytes(b))
	}
	if c := num(e.Data, "conflicts"); c > 0 {
		parts = append(parts, fmt.Sprintf("冲突 %d 个", c))
	}
	if x := num(e.Data, "exhausted"); x > 0 {
		parts = append(parts, fmt.Sprintf("%d 个已用尽重试需人工处理", x))
	}
	t := title("开始备份")
	if created+requeued == 0 {
		t = title("无需备份")
		parts = []string{"没有新文件"}
		if c := num(e.Data, "conflicts"); c > 0 {
			parts = append(parts, fmt.Sprintf("冲突 %d 个待处理", c))
		}
	}
	return Message{Title: t, Body: strings.Join(parts, "，"), URL: siteURL}
}

func formatBackupFinished(e events.Event, siteURL string) Message {
	completed, failed, interrupted, cancelled := num(e.Data, "completed"), num(e.Data, "failed"), num(e.Data, "interrupted"), num(e.Data, "cancelled")
	parts := []string{fmt.Sprintf("成功 %d 个文件 / %s", completed, humanBytes(num(e.Data, "bytes")))}
	if d := num(e.Data, "duration_sec"); d > 0 {
		parts = append(parts, "用时 "+humanDuration(time.Duration(d)*time.Second))
	}
	t := title("备份完成")
	if failed > 0 || interrupted > 0 {
		t = title("备份完成（有失败）")
		parts = append(parts, fmt.Sprintf("失败 %d 个，中断 %d 个", failed, interrupted))
	}
	if cancelled > 0 {
		parts = append(parts, fmt.Sprintf("取消 %d 个", cancelled))
	}
	return Message{Title: t, Body: strings.Join(parts, "，"), URL: siteURL}
}

func formatFailures(n int, last, siteURL string) Message {
	b := fmt.Sprintf("%d 个文件备份失败或中断", n)
	if last != "" {
		b += "。最近一条：" + last
	}
	return Message{Title: title("备份失败"), Body: b, URL: siteURL}
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func boolean(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	b, _ := m[k].(bool)
	return b
}

// num 兼容 int / int64 / float64（事件可能来自 JSON 反序列化）。
func num(m map[string]any, k string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%d 小时 %d 分 %d 秒", h, m, s)
	case m > 0:
		return fmt.Sprintf("%d 分 %d 秒", m, s)
	}
	return fmt.Sprintf("%d 秒", s)
}
