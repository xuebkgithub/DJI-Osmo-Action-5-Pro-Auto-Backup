// Package fsinfo 为各平台 probe 共享的类型与匹配规则，避免 platform 与子包循环引用。
package fsinfo

import "strings"

// Expectation 期望的目标挂载。
type Expectation struct {
	// Kind: cifs | smbfs | network | local
	Kind string
	// Share 期望的来源，如 //192.168.1.100/VideoBackup；为空则不比对。
	Share string
}

// Info 探测结果。
type Info struct {
	Path      string
	FSType    string
	Source    string
	FreeBytes uint64
	Matched   bool
}

// Matches 判断探测到的文件系统是否满足期望。
// kind: cifs | smbfs | network（cifs 或 smbfs 均可）| local。
// mountedHere 表示 path 本身就是挂载点而不是挂载点下的子目录；对网络挂载要求为 true，
// 这样 NAS 掉线后残留的本地目录不会被误判。
func Matches(expect Expectation, fstype, source string, mountedHere bool) bool {
	fstype = strings.ToLower(fstype)
	switch expect.Kind {
	case "local":
		return true
	case "cifs":
		if fstype != "cifs" {
			return false
		}
	case "smbfs":
		if fstype != "smbfs" {
			return false
		}
	case "network":
		if fstype != "cifs" && fstype != "smbfs" {
			return false
		}
	default:
		return false
	}
	if !mountedHere {
		return false
	}
	return ShareMatches(expect.Share, source)
}

// ShareMatches 比较配置的共享与挂载来源。
// 配置形如 //192.168.1.100/VideoBackup；来源可能形如 //user@192.168.1.100/VideoBackup（mac）
// 或 //192.168.1.100/VideoBackup（Linux）。
func ShareMatches(want, source string) bool {
	if strings.TrimSpace(want) == "" {
		return true
	}
	wh, ws := normShare(want)
	sh, ss := normShare(source)
	return wh == sh && ws == ss
}

func normShare(s string) (host, share string) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "//"), `\\`)
	s = strings.ReplaceAll(s, `\`, "/")
	if at := strings.LastIndex(s, "@"); at >= 0 {
		if slash := strings.Index(s, "/"); slash < 0 || at < slash {
			s = s[at+1:]
		}
	}
	host, share, _ = strings.Cut(s, "/")
	return strings.ToLower(host), strings.ToLower(strings.TrimSuffix(share, "/"))
}
