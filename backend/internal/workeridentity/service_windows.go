//go:build windows

package workeridentity

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func installLauncherService(cfg Config, configPath string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("worker identity: connect service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(cfg.ServiceName)
	if err == nil {
		defer service.Close()
		current, configErr := service.Config()
		if configErr != nil {
			return fmt.Errorf("worker identity: query launcher service config: %w", configErr)
		}
		desiredBinary := windows.ComposeCommandLine([]string{cfg.LauncherPath, "service", "--config", configPath})
		needsUpdate := !strings.EqualFold(current.BinaryPathName, desiredBinary) || current.StartType != mgr.StartAutomatic ||
			!strings.EqualFold(current.ServiceStartName, "LocalSystem")
		status, queryErr := service.Query()
		if queryErr != nil {
			return queryErr
		}
		if needsUpdate && status.State != svc.Stopped {
			if _, err := service.Control(svc.Stop); err != nil {
				return fmt.Errorf("worker identity: stop launcher service for update: %w", err)
			}
			if err := waitServiceState(service, svc.Stopped, 10*time.Second); err != nil {
				return err
			}
			status.State = svc.Stopped
		}
		if needsUpdate {
			current.BinaryPathName = desiredBinary
			current.StartType = mgr.StartAutomatic
			current.ServiceStartName = "LocalSystem"
			if err := service.UpdateConfig(current); err != nil {
				return fmt.Errorf("worker identity: update launcher service: %w", err)
			}
		}
		if status.State != svc.Running {
			if err := service.Start(); err != nil {
				return fmt.Errorf("worker identity: start launcher service: %w", err)
			}
			return waitServiceState(service, svc.Running, 10*time.Second)
		}
		return nil
	}
	service, err = manager.CreateService(cfg.ServiceName, cfg.LauncherPath, mgr.Config{
		DisplayName: "Agent Orchestrator Worker Launcher", Description: "Starts isolated AO workers using a dedicated non-administrator identity.",
		StartType: mgr.StartAutomatic,
	}, "service", "--config", configPath)
	if err != nil {
		return fmt.Errorf("worker identity: create launcher service: %w", err)
	}
	defer service.Close()
	if err := service.Start(); err != nil {
		return fmt.Errorf("worker identity: start launcher service: %w", err)
	}
	return waitServiceState(service, svc.Running, 10*time.Second)
}

func waitServiceState(service *mgr.Service, wanted svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, queryErr := service.Query()
		if queryErr != nil {
			return queryErr
		}
		if status.State == wanted {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("worker identity: launcher service did not reach state %d: %w", wanted, errors.New("timeout"))
}
