// Package plist 解析 Apple XML 属性列表（diskutil、ioreg 的 -plist / -a 输出）。
// 只依赖标准库，支持 dict、array、string、integer、real、true、false、data、date。
package plist

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Parse 解析一个 plist 文档并返回顶层值：map[string]any、[]any、string、int64、float64、bool、[]byte 或 time.Time。
func Parse(r io.Reader) (any, error) {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("plist: no value found")
			}
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "plist" {
			continue
		}
		return parseValue(dec, se)
	}
}

func parseValue(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		return parseDict(dec)
	case "array":
		return parseArray(dec)
	case "string":
		return readText(dec, se.Name.Local)
	case "integer":
		s, err := readText(dec, se.Name.Local)
		if err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s)
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v, nil
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return int64(u), nil
		}
		return nil, fmt.Errorf("plist: bad integer %q", s)
	case "real":
		s, err := readText(dec, se.Name.Local)
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("plist: bad real %q", s)
		}
		return f, nil
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	case "data":
		s, err := readText(dec, se.Name.Local)
		if err != nil {
			return nil, err
		}
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
				return -1
			}
			return r
		}, s)
		b, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return nil, fmt.Errorf("plist: bad data: %w", err)
		}
		return b, nil
	case "date":
		s, err := readText(dec, se.Name.Local)
		if err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("plist: bad date %q", s)
		}
		return t, nil
	}
	return nil, fmt.Errorf("plist: unexpected element <%s>", se.Name.Local)
}

func parseDict(dec *xml.Decoder) (map[string]any, error) {
	out := map[string]any{}
	var key string
	haveKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				k, err := readText(dec, "key")
				if err != nil {
					return nil, err
				}
				key, haveKey = k, true
				continue
			}
			if !haveKey {
				return nil, fmt.Errorf("plist: value <%s> without key", t.Name.Local)
			}
			v, err := parseValue(dec, t)
			if err != nil {
				return nil, err
			}
			out[key] = v
			haveKey = false
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return out, nil
			}
		}
	}
}

func parseArray(dec *xml.Decoder) ([]any, error) {
	out := []any{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			v, err := parseValue(dec, t)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		case xml.EndElement:
			if t.Name.Local == "array" {
				return out, nil
			}
		}
	}
}

func readText(dec *xml.Decoder, name string) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			if t.Name.Local == name {
				return b.String(), nil
			}
		case xml.StartElement:
			return "", fmt.Errorf("plist: unexpected <%s> inside <%s>", t.Name.Local, name)
		}
	}
}

// Dict 断言为字典。
func Dict(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// String 取字典中的字符串。
func String(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// Int 取字典中的整数（integer 或 real）。
func Int(m map[string]any, key string) (int64, bool) {
	switch v := m[key].(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}

// Bool 取字典中的布尔值。
func Bool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

// Array 取字典中的数组。
func Array(m map[string]any, key string) []any {
	a, _ := m[key].([]any)
	return a
}
