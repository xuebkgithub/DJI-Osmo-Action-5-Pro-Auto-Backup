package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"action-backup/internal/api/auth"
	"action-backup/internal/config"
)

// runPasswd 实现 `action-backup passwd`：交互式读取口令，bcrypt cost 12 写入配置文件。
// 不接受命令行明文口令；非终端输入必须显式 --stdin。
func runPasswd(args []string, stdin io.Reader, stderr io.Writer) error {
	fs := flag.NewFlagSet("action-backup passwd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "/etc/action-backup/config.yaml", "配置文件路径")
	username := fs.String("username", "", "同时设置 server.auth.username（可选）")
	fromStdin := fs.Bool("stdin", false, "从标准输入读取一行作为口令（脚本用）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("passwd does not accept positional arguments; the password is read interactively or with --stdin")
	}
	if _, err := config.Load(*cfgPath); err != nil {
		return err
	}

	var password string
	switch {
	case *fromStdin:
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read password: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
	default:
		f, ok := stdin.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) {
			return errors.New("stdin is not a terminal; use --stdin to read the password from a pipe")
		}
		_, _ = fmt.Fprint(stderr, "New password: ")
		p1, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		_, _ = fmt.Fprint(stderr, "Repeat password: ")
		p2, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if string(p1) != string(p2) {
			return errors.New("passwords do not match")
		}
		password = string(p1)
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	values := map[string]any{"server.auth.password_hash": hash}
	if strings.TrimSpace(*username) != "" {
		values["server.auth.username"] = strings.TrimSpace(*username)
	}
	if err := config.SetValues(*cfgPath, values); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "password hash written to %s (user %q)\n", cfg.Path, cfg.Server.Auth.Username)
	if !cfg.Server.Auth.Enabled {
		_, _ = fmt.Fprintln(stderr, "note: server.auth.enabled is false in this file; set it to true to require login")
	}
	return nil
}
