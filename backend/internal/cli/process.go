package cli

import (
	"os"
	"os/exec"
)

type processStartConfig struct {
	Path         string
	Args         []string
	Env          []string
	Stdin        *os.File
	Stdout       *os.File
	Stderr       *os.File
	Detach       bool
	DetachStdio  bool
	ReleaseAfter bool
}

func startProcess(cfg processStartConfig) error {
	cmd := exec.Command(cfg.Path, cfg.Args...)
	cmd.Env = cfg.Env
	cmd.Stdin = cfg.Stdin
	cmd.Stdout = cfg.Stdout
	cmd.Stderr = cfg.Stderr

	var devNull *os.File
	if cfg.DetachStdio && (cmd.Stdin == nil || cmd.Stdout == nil || cmd.Stderr == nil) {
		var err error
		devNull, err = os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer func() { _ = devNull.Close() }()
		if cmd.Stdin == nil {
			cmd.Stdin = devNull
		}
		if cmd.Stdout == nil {
			cmd.Stdout = devNull
		}
		if cmd.Stderr == nil {
			cmd.Stderr = devNull
		}
	}

	if cfg.Detach {
		// Detach long-running children into their own session/process group so
		// they do not receive terminal signals sent to the launcher.
		cmd.SysProcAttr = detachSysProcAttr()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if cfg.ReleaseAfter {
		return cmd.Process.Release()
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
