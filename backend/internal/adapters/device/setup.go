package device

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	androidToolsVersion = "15859902"
	androidAPILevel     = "36"
	androidAVDName      = "AO_Pixel_API_36"
	androidRequired     = int64(12 << 30)
	iosRequired         = int64(20 << 30)
	androidLicenseURL   = "https://developer.android.com/studio/terms"
	appleLicenseURL     = "https://www.apple.com/legal/sla/docs/xcode.pdf"
	xcodeURL            = "https://developer.apple.com/xcode/"
)

type androidArchive struct {
	URL, SHA256, Arch string
	Size              int64
}

func currentAndroidArchive() (androidArchive, error) {
	switch runtime.GOARCH {
	case "arm64":
		return androidArchive{
			URL:    "https://dl.google.com/android/repository/commandlinetools-mac_arm64-15859902_latest.zip",
			SHA256: "835b62a26162b229b441d1f6d4680383815a270809eb33522c0d480fa5002c4e", Arch: "arm64-v8a", Size: 156_100_000,
		}, nil
	case "amd64":
		return androidArchive{
			URL:    "https://dl.google.com/android/repository/commandlinetools-mac_x86_64-15859902_latest.zip",
			SHA256: "c5a6378ab5cf7e0d5701921405115befff13e9ff7417fb588389338f8bd050f3", Arch: "x86_64", Size: 156_300_000,
		}, nil
	default:
		return androidArchive{}, setupError("HOST_ARCH_UNSUPPORTED", "AO does not have an Android toolchain for this Mac architecture", "")
	}
}

// SetupPlan performs only local readiness checks. Install performs all writes.
func (r *Runtime) SetupPlan(ctx context.Context, platform domain.DevicePlatform) (ports.DeviceSetupPlan, error) {
	if r.goos != "darwin" {
		return ports.DeviceSetupPlan{}, setupError("HOST_PLATFORM_UNSUPPORTED", "Managed virtual-device setup is currently available only on macOS", "")
	}
	available := availableBytes(ctx, r.dataDir)
	switch platform {
	case domain.DevicePlatformAndroid:
		_, err := currentAndroidArchive()
		if err != nil {
			return ports.DeviceSetupPlan{}, err
		}
		ready := regularFile(filepath.Join(r.androidSDKDir(), "platform-tools", "adb")) &&
			regularFile(filepath.Join(r.androidSDKDir(), "emulator", "emulator")) &&
			regularFile(filepath.Join(r.androidAVDDir(), androidAVDName+".avd", "config.ini"))
		return ports.DeviceSetupPlan{Ready: ready, State: stateForReady(ready), RequiredBytes: androidRequired,
			AvailableBytes: available, LicenseURL: androidLicenseURL, InstalledVersion: androidToolsVersion,
			Message: fmt.Sprintf("Android API %s · command-line tools %s · about %.1f GB", androidAPILevel, androidToolsVersion, float64(androidRequired)/(1<<30)),
		}, nil
	case domain.DevicePlatformIOS:
		plan := ports.DeviceSetupPlan{State: domain.DeviceSetupIdle, RequiredBytes: iosRequired, AvailableBytes: available,
			LicenseURL: appleLicenseURL, Message: "Latest iOS Simulator runtime managed by the installed Xcode"}
		if _, err := r.lookPath("xcodebuild"); err != nil || !xcodeDeveloperDir(ctx) {
			plan.State, plan.ActionURL, plan.Message = domain.DeviceSetupAwaitingAction, xcodeURL, "Install the full Xcode app, then return here to continue"
			return plan, nil
		}
		if err := exec.CommandContext(ctx, "xcodebuild", "-checkFirstLaunchStatus").Run(); err != nil {
			plan.State, plan.ActionURL, plan.Message = domain.DeviceSetupAwaitingAction, xcodeURL, "Open Xcode and complete its license and first-launch setup, then retry"
			return plan, nil
		}
		plan.Ready = iosSimulatorReady(ctx)
		plan.State = stateForReady(plan.Ready)
		return plan, nil
	default:
		return ports.DeviceSetupPlan{}, setupError("INVALID_ARGUMENT", "Platform must be ios or android", "")
	}
}

func stateForReady(ready bool) domain.DeviceSetupState {
	if ready {
		return domain.DeviceSetupSucceeded
	}
	return domain.DeviceSetupIdle
}

// Install performs the fixed, platform-specific managed setup plan.
func (r *Runtime) Install(ctx context.Context, platform domain.DevicePlatform, progress func(ports.DeviceSetupProgress)) error {
	plan, err := r.SetupPlan(ctx, platform)
	if err != nil {
		return err
	}
	if plan.Ready {
		return nil
	}
	if plan.State == domain.DeviceSetupAwaitingAction {
		return setupError("USER_ACTION_REQUIRED", plan.Message, plan.ActionURL)
	}
	if plan.AvailableBytes > 0 && plan.AvailableBytes < plan.RequiredBytes {
		return setupError("DISK_SPACE_REQUIRED", fmt.Sprintf("Setup needs %.1f GB free; %.1f GB is available", float64(plan.RequiredBytes)/(1<<30), float64(plan.AvailableBytes)/(1<<30)), "")
	}
	switch platform {
	case domain.DevicePlatformAndroid:
		return r.installAndroid(ctx, progress)
	case domain.DevicePlatformIOS:
		return r.installIOS(ctx, progress)
	default:
		return setupError("INVALID_ARGUMENT", "Platform must be ios or android", "")
	}
}

func (r *Runtime) installAndroid(ctx context.Context, report func(ports.DeviceSetupProgress)) error {
	archive, err := currentAndroidArchive()
	if err != nil {
		return err
	}
	root := r.androidRoot()
	downloads, staging := filepath.Join(root, "downloads"), filepath.Join(root, ".staging")
	if err := os.MkdirAll(downloads, 0o700); err != nil {
		return fmt.Errorf("create Android download directory: %w", err)
	}
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return fmt.Errorf("create Android staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	toolsArchive := filepath.Join(downloads, filepath.Base(archive.URL)+".part")
	report(ports.DeviceSetupProgress{State: domain.DeviceSetupDownloading, Stage: "android-tools", Message: "Downloading verified Android command-line tools", Progress: 2, TotalBytes: archive.Size})
	if err := downloadResumable(ctx, archive.URL, archive.SHA256, toolsArchive, func(done, total int64) {
		report(ports.DeviceSetupProgress{State: domain.DeviceSetupDownloading, Stage: "android-tools", Message: "Downloading verified Android command-line tools", Progress: percentRange(done, total, 2, 12), DownloadedBytes: done, TotalBytes: total})
	}); err != nil {
		return err
	}

	report(ports.DeviceSetupProgress{State: domain.DeviceSetupInstalling, Stage: "java", Message: "Preparing AO-managed Java runtime", Progress: 13})
	javaHome, javaVersion, err := r.ensureManagedJDK(ctx, downloads, staging, report)
	if err != nil {
		return err
	}

	toolsStage := filepath.Join(staging, "tools")
	if err := unzipSafe(toolsArchive, toolsStage); err != nil {
		return setupError("ARCHIVE_INVALID", "The downloaded Android tools archive could not be extracted", "")
	}
	source := filepath.Join(toolsStage, "cmdline-tools")
	target := filepath.Join(r.androidSDKDir(), "cmdline-tools", "latest")
	if err := replaceDir(source, target); err != nil {
		return fmt.Errorf("install Android command-line tools: %w", err)
	}

	env := r.androidInstallEnv(javaHome)
	sdkmanager := filepath.Join(target, "bin", "sdkmanager")
	image := "system-images;android-" + androidAPILevel + ";default;" + archive.Arch
	packages := []string{"platform-tools", "emulator", "platforms;android-" + androidAPILevel, image}
	report(ports.DeviceSetupProgress{State: domain.DeviceSetupInstalling, Stage: "android-packages", Message: "Installing Android emulator, platform tools, and system image", Progress: 28})
	if err := runStreaming(ctx, sdkmanager, append([]string{"--sdk_root=" + r.androidSDKDir()}, packages...), strings.Repeat("y\n", 100), env, func(line string) {
		if value, ok := parsePercent(line); ok {
			report(ports.DeviceSetupProgress{State: domain.DeviceSetupInstalling, Stage: "android-packages", Message: "Installing Android packages", Progress: 28 + value*55/100})
		}
	}); err != nil {
		return setupError("ANDROID_PACKAGE_INSTALL_FAILED", "Android packages could not be installed. Retry to resume the setup.", "")
	}

	report(ports.DeviceSetupProgress{State: domain.DeviceSetupCreating, Stage: "android-avd", Message: "Creating AO Android virtual device", Progress: 88})
	if err := os.MkdirAll(r.androidAVDDir(), 0o700); err != nil {
		return err
	}
	avdmanager := filepath.Join(target, "bin", "avdmanager")
	if err := runStreaming(ctx, avdmanager, []string{"create", "avd", "--force", "--name", androidAVDName, "--package", image, "--device", "pixel_8"}, "no\n", env, nil); err != nil {
		return setupError("ANDROID_AVD_CREATE_FAILED", "AO could not create the Android virtual device", "")
	}
	report(ports.DeviceSetupProgress{State: domain.DeviceSetupVerifying, Stage: "verify", Message: "Verifying Android device tools", Progress: 96, InstalledVersion: androidToolsVersion + " / " + javaVersion})
	if !regularFile(filepath.Join(r.androidSDKDir(), "platform-tools", "adb")) || !regularFile(filepath.Join(r.androidAVDDir(), androidAVDName+".avd", "config.ini")) {
		return setupError("ANDROID_VERIFY_FAILED", "Android setup completed but its tools or virtual device could not be verified", "")
	}
	return nil
}

func (r *Runtime) ensureManagedJDK(ctx context.Context, downloads, staging string, report func(ports.DeviceSetupProgress)) (string, string, error) {
	managed := filepath.Join(r.androidRoot(), "jdk")
	if java := findNamedFile(managed, "java"); java != "" {
		return filepath.Dir(filepath.Dir(java)), "Temurin 21", nil
	}
	arch := "aarch64"
	if runtime.GOARCH == "amd64" {
		arch = "x64"
	}
	metadataURL := "https://api.adoptium.net/v3/assets/latest/21/hotspot?architecture=" + arch + "&image_type=jdk&os=mac&vendor=eclipse"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", setupError("DOWNLOAD_FAILED", "AO could not reach the Java runtime vendor", "")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", setupError("DOWNLOAD_FAILED", "The Java runtime vendor returned an unexpected response", "")
	}
	var assets []struct {
		Binary struct {
			Package struct {
				Link, Checksum, Name string
				Size                 int64
			} `json:"package"`
		} `json:"binary"`
		Version struct {
			Semver string `json:"semver"`
		} `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&assets); err != nil || len(assets) == 0 || assets[0].Binary.Package.Link == "" || assets[0].Binary.Package.Checksum == "" {
		return "", "", setupError("DOWNLOAD_METADATA_INVALID", "AO could not verify Java runtime download metadata", "")
	}
	pkg := assets[0].Binary.Package
	archive := filepath.Join(downloads, "temurin-21.tar.gz.part")
	if err := downloadResumable(ctx, pkg.Link, pkg.Checksum, archive, func(done, total int64) {
		report(ports.DeviceSetupProgress{State: domain.DeviceSetupDownloading, Stage: "java", Message: "Downloading verified AO-managed Java runtime", Progress: percentRange(done, total, 13, 26), DownloadedBytes: done, TotalBytes: total})
	}); err != nil {
		return "", "", err
	}
	jdkStage := filepath.Join(staging, "jdk")
	if err := untarGzipSafe(archive, jdkStage); err != nil {
		return "", "", setupError("ARCHIVE_INVALID", "The downloaded Java runtime archive could not be extracted", "")
	}
	if findNamedFile(jdkStage, "java") == "" {
		return "", "", setupError("ARCHIVE_INVALID", "The Java runtime archive did not contain Java", "")
	}
	if err := replaceDir(jdkStage, managed); err != nil {
		return "", "", err
	}
	java := findNamedFile(managed, "java")
	return filepath.Dir(filepath.Dir(java)), assets[0].Version.Semver, nil
}

func (r *Runtime) androidInstallEnv(javaHome string) []string {
	return mergeEnvironment(deviceHostEnv(), []string{"ANDROID_HOME=" + r.androidSDKDir(), "ANDROID_SDK_ROOT=" + r.androidSDKDir(),
		"ANDROID_AVD_HOME=" + r.androidAVDDir(), "JAVA_HOME=" + javaHome,
		"PATH=" + strings.Join([]string{filepath.Join(javaHome, "bin"), filepath.Join(r.androidSDKDir(), "platform-tools"), filepath.Join(r.androidSDKDir(), "emulator"), os.Getenv("PATH")}, string(os.PathListSeparator))})
}

func (r *Runtime) installIOS(ctx context.Context, report func(ports.DeviceSetupProgress)) error {
	report(ports.DeviceSetupProgress{State: domain.DeviceSetupDownloading, Stage: "ios-runtime", Message: "Downloading the iOS Simulator runtime with Xcode", Progress: 5})
	if err := runStreaming(ctx, "xcodebuild", []string{"-downloadPlatform", "iOS"}, "", deviceHostEnv(), func(line string) {
		if value, ok := parsePercent(line); ok {
			report(ports.DeviceSetupProgress{State: domain.DeviceSetupDownloading, Stage: "ios-runtime", Message: "Downloading the iOS Simulator runtime with Xcode", Progress: 5 + value*80/100})
		}
	}); err != nil {
		return setupError("IOS_RUNTIME_DOWNLOAD_FAILED", "Xcode could not download the iOS Simulator runtime. Check Xcode and retry.", xcodeURL)
	}
	report(ports.DeviceSetupProgress{State: domain.DeviceSetupCreating, Stage: "ios-device", Message: "Creating an AO iPhone Simulator", Progress: 90})
	if !iosHasDevice(ctx) {
		deviceType := firstIPhoneDeviceType(ctx)
		if deviceType == "" {
			return setupError("IOS_DEVICE_TYPE_MISSING", "Xcode did not report an available iPhone Simulator device type", xcodeURL)
		}
		if err := runStreaming(ctx, "xcrun", []string{"simctl", "create", "AO iPhone", deviceType}, "", deviceHostEnv(), nil); err != nil {
			return setupError("IOS_DEVICE_CREATE_FAILED", "Xcode could not create the AO iPhone Simulator", xcodeURL)
		}
	}
	report(ports.DeviceSetupProgress{State: domain.DeviceSetupVerifying, Stage: "verify", Message: "Verifying iOS Simulator", Progress: 97})
	if !iosSimulatorReady(ctx) {
		return setupError("IOS_VERIFY_FAILED", "The iOS Simulator runtime or device could not be verified", xcodeURL)
	}
	return nil
}

func xcodeDeveloperDir(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "xcode-select", "-p").Output()
	return err == nil && strings.Contains(string(out), ".app/Contents/Developer")
}

func iosSimulatorReady(ctx context.Context) bool { return iosHasRuntime(ctx) && iosHasDevice(ctx) }
func iosHasRuntime(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "list", "runtimes", "--json").Output()
	if err != nil {
		return false
	}
	var value struct {
		Runtimes []struct {
			Identifier  string
			IsAvailable bool `json:"isAvailable"`
		} `json:"runtimes"`
	}
	if json.Unmarshal(out, &value) != nil {
		return false
	}
	for _, item := range value.Runtimes {
		if item.IsAvailable && strings.Contains(strings.ToLower(item.Identifier), "ios") {
			return true
		}
	}
	return false
}
func iosHasDevice(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "list", "devices", "available", "--json").Output()
	if err != nil {
		return false
	}
	var value struct {
		Devices map[string][]json.RawMessage `json:"devices"`
	}
	return json.Unmarshal(out, &value) == nil && len(value.Devices) > 0 && func() bool {
		for key, devices := range value.Devices {
			if strings.Contains(strings.ToLower(key), "ios") && len(devices) > 0 {
				return true
			}
		}
		return false
	}()
}
func firstIPhoneDeviceType(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "list", "devicetypes", "--json").Output()
	if err != nil {
		return ""
	}
	var value struct {
		DeviceTypes []struct{ Name, Identifier string } `json:"devicetypes"`
	}
	if json.Unmarshal(out, &value) != nil {
		return ""
	}
	for _, item := range value.DeviceTypes {
		if strings.HasPrefix(item.Name, "iPhone") {
			return item.Identifier
		}
	}
	return ""
}

func availableBytes(ctx context.Context, path string) int64 {
	out, err := exec.CommandContext(ctx, "df", "-Pk", path).Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0
	}
	blocks, _ := strconv.ParseInt(fields[3], 10, 64)
	return blocks * 1024
}

func downloadResumable(ctx context.Context, url, expectedHash, target string, onProgress func(int64, int64)) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	offset := int64(0)
	if info, err := os.Stat(target); err == nil {
		offset = info.Size()
		if actual, hashErr := sha256File(target); hashErr == nil && strings.EqualFold(actual, expectedHash) {
			if onProgress != nil {
				onProgress(offset, offset)
			}
			return nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return setupError("DOWNLOAD_FAILED", "Download failed. Check the network and retry to resume.", "")
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		offset = 0
		flags |= os.O_TRUNC
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return setupError("DOWNLOAD_FAILED", fmt.Sprintf("Vendor download returned HTTP %d", resp.StatusCode), "")
	}
	file, err := os.OpenFile(target, flags, 0o600)
	if err != nil {
		return err
	}
	total := offset + resp.ContentLength
	reader := &progressReader{reader: resp.Body, done: offset, total: total, report: onProgress}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return setupError("DOWNLOAD_FAILED", "Download was interrupted. Retry to resume.", "")
	}
	if closeErr != nil {
		return closeErr
	}
	actual, err := sha256File(target)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expectedHash) {
		_ = os.Remove(target)
		return setupError("DOWNLOAD_CHECKSUM_MISMATCH", "The vendor download failed integrity verification and was removed", "")
	}
	return nil
}

type progressReader struct {
	reader      io.Reader
	done, total int64
	report      func(int64, int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.done += int64(n)
	if r.report != nil {
		r.report(r.done, r.total)
	}
	return n, err
}
func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runStreaming(ctx context.Context, command string, args []string, stdin string, env []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env, cmd.Stdin = env, strings.NewReader(stdin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	consume := func(reader io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			if onLine != nil {
				onLine(scanner.Text())
			}
		}
	}
	wg.Add(2)
	go consume(stdout)
	go consume(stderr)
	wg.Wait()
	return cmd.Wait()
}

func parsePercent(line string) (int, bool) {
	for _, field := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '[' || r == ']' || r == '%' || r == '=' }) {
		if n, err := strconv.Atoi(strings.TrimSpace(field)); err == nil && n >= 0 && n <= 100 && strings.Contains(line, field+"%") {
			return n, true
		}
	}
	return 0, false
}
func percentRange(done, total int64, from, to int) int {
	if total <= 0 {
		return from
	}
	value := from + int(done)*(to-from)/int(total)
	if value > to {
		return to
	}
	return value
}

func replaceDir(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	backup := target + ".old"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(source, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}
func safeJoin(root, name string) (string, error) {
	target := filepath.Join(root, filepath.Clean(name))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("archive path escapes destination")
	}
	return target, nil
}
func unzipSafe(path, destination string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, item := range reader.File {
		target, err := safeJoin(destination, item.Name)
		if err != nil {
			return err
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if item.Mode()&os.ModeSymlink != 0 {
			return errors.New("zip symlinks are not supported")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		in, err := item.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, item.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(in, 2<<30))
		in.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}
func untarGzipSafe(path, destination string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destination, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode).Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, io.LimitReader(reader, 2<<30))
			out.Close()
			if copyErr != nil {
				return copyErr
			}
		case tar.TypeSymlink:
			linkTarget := filepath.Clean(filepath.Join(filepath.Dir(target), header.Linkname))
			relRoot, err := filepath.Rel(destination, linkTarget)
			if err != nil || relRoot == ".." || strings.HasPrefix(relRoot, ".."+string(filepath.Separator)) {
				return errors.New("archive link escapes destination")
			}
			rel, _ := filepath.Rel(filepath.Dir(target), linkTarget)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.Symlink(rel, target); err != nil {
				return err
			}
		}
	}
}
func findNamedFile(root, name string) string {
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == name && filepath.Base(filepath.Dir(path)) == "bin" {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
func setupError(code, message, actionURL string) error {
	return &ports.DeviceSetupRuntimeError{Code: code, Message: message, ActionURL: actionURL, Actionable: actionURL != ""}
}
