package update

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// gitDescribeSuffix 匹配 git describe 在 tag 之后追加的「提交数-g哈希」，如 v0.8.0-3-g76121de。
var gitDescribeSuffix = regexp.MustCompile(`(^|-)\d+-g[0-9a-f]{7,40}$`)

// canonical 把 "0.8.0" / "v0.8.0" 规整成 semver 包要求的 "v0.8.0"；不是合法语义化版本返回空串。
func canonical(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// displayVersion 去掉前导 v，供界面与文件名使用。
func displayVersion(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// IsDevBuild 判断版本串是否来自非正式构建：无法解析、带 -dirty / -dev、或是 git describe 的「tag 之后若干提交」形式。
// 这类构建在语义化版本里会被当成对应正式版的预发布（v0.8.0-dirty < v0.8.0），
// 直接比较会把同一份代码提示成「有新版本」，所以开发构建不参与更新判定，也不做定期检查。
func IsDevBuild(v string) bool {
	c := canonical(v)
	if c == "" {
		return true
	}
	pre := strings.TrimPrefix(semver.Prerelease(c), "-")
	if pre == "" {
		return false
	}
	if pre == "dirty" || strings.HasSuffix(pre, "-dirty") {
		return true
	}
	if pre == "dev" || strings.HasSuffix(pre, "-dev") || strings.HasSuffix(pre, ".dev") {
		return true
	}
	return gitDescribeSuffix.MatchString(pre)
}

// newer 判断 candidate 是否严格高于 current；任一方不是合法版本返回 false。
func newer(candidate, current string) bool {
	a, b := canonical(candidate), canonical(current)
	if a == "" || b == "" {
		return false
	}
	return semver.Compare(a, b) > 0
}

// isPrereleaseVersion 版本号自身带预发布段（如 0.9.0-rc.1）。
func isPrereleaseVersion(v string) bool {
	c := canonical(v)
	return c != "" && semver.Prerelease(c) != ""
}
