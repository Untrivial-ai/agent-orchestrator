// Package cua controls one launch-owned window through Cua Driver 0.34.0.
// It never exposes Cua's MCP server or permits desktop-scope operations.
package cua

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/image/draw"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// ErrRefused means validation prevented input dispatch.
var ErrRefused = errors.New("cua input refused")

// ErrProvider means a driver operation failed; its effect can be unknown.
var ErrProvider = errors.New("cua driver failed")

// Error retains stable refusal/provider codes without claiming an app effect.
type Error struct {
	Code   string
	Detail string
	cause  error
}

func (e *Error) Error() string { return "cua " + e.Code + ": " + e.Detail }

// Unwrap distinguishes pre-dispatch validation from provider failures.
func (e *Error) Unwrap() error { return e.cause }

func refuse(code, detail string) error {
	return &Error{Code: code, Detail: detail, cause: ErrRefused}
}

// DeliveryMode selects the policy for clicks, typing and keys.
// Foreground can interrupt the current app and must be journaled before input.
type DeliveryMode string

// Foreground is the supported input policy. Background is retained as a
// recognized legacy value so callers can reject it explicitly.
const (
	Background DeliveryMode = "background"
	Foreground DeliveryMode = "foreground"
)

// Config is supplied by the supervising daemon, never by worker tool arguments.
// DataDir is its resolved AO data directory, not the target's data directory.
type Config struct {
	DataDir      string
	AppPath      string
	DeliveryMode DeliveryMode
	CaptureTTL   time.Duration
	// Runner and ProcessStartedAt allow deterministic boundary tests. Production
	// leaves both nil. Process timestamps must match the target adapter exactly.
	Runner           Runner
	ProcessStartedAt func(context.Context, int) (time.Time, error)
}

type binding struct {
	ready, sessionIssued bool
	releaseErr           error
	target               domain.TestTargetIdentity
	session              string
	receipt              *captureReceipt
	lastTyped            *typedFocus
	recording            *windowRecording
}

type typedFocus struct {
	point  pixel
	bounds domain.TestWindowBounds
}

type captureReceipt struct {
	frame         domain.TestDesktopFrame
	providerID    string
	screenshotID  string
	keyToken      string
	elements      []capturedElement
	addressable   map[string]capturedElement
	width, height int // original provider capture pixels, never worker-supplied
}

type captureState struct {
	PID              int                     `json:"pid"`
	WindowID         int                     `json:"window_id"`
	Bounds           domain.TestWindowBounds `json:"window_bounds"`
	Width            int                     `json:"screenshot_width"`
	Height           int                     `json:"screenshot_height"`
	Scale            float64                 `json:"screenshot_scale"`
	Capture          string                  `json:"capture_id"`
	Elements         []capturedElement       `json:"elements"`
	Truncated        bool                    `json:"truncated"`
	ElementsComplete *bool                   `json:"elements_complete"`
}

type capturedElement struct {
	Role    string          `json:"role"`
	Label   string          `json:"label"`
	Title   string          `json:"title"`
	Value   json.RawMessage `json:"value"`
	Enabled *bool           `json:"enabled"`
	Focused *bool           `json:"focused"`
	Visible *bool           `json:"visible"`
	Token   string          `json:"element_token"`
	Frame   *pixelBounds    `json:"screenshot_frame"`
}

type pixelBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"w"`
	Height float64 `json:"h"`
}

// Adapter serializes observation and input. The supervising daemon owns one
// instance, calls Close on shutdown, and must journal DeliveryMode before input.
type Adapter struct {
	mu            sync.Mutex
	cfg           Config
	runner        Runner
	started       func(context.Context, int) (time.Time, error)
	root          string
	controlRoot   string
	bindings      map[string]*binding
	released      map[string]*binding
	driver        driverIdentity
	pendingDriver driverIdentity
	launchIssued  bool
	closed        bool
	now           func() time.Time
	startRecorder func(ctx context.Context, args, env []string, stdout, stderr string) (*recordingProcess, error)
	interruptWait time.Duration
	terminateWait time.Duration
}

var _ ports.TestingDesktopControl = (*Adapter)(nil)

// New validates config without starting a process or triggering permissions.
func New(cfg Config) (*Adapter, error) {
	if !filepath.IsAbs(cfg.DataDir) {
		return nil, refuse("invalid_config", "DataDir must be an absolute resolved AO data directory")
	}
	if cfg.AppPath == "" {
		cfg.AppPath = "/Applications/CuaDriver.app"
	}
	if !filepath.IsAbs(cfg.AppPath) || filepath.Ext(cfg.AppPath) != ".app" {
		return nil, refuse("invalid_config", "AppPath must name the signed CuaDriver.app bundle")
	}
	if cfg.DeliveryMode == "" {
		cfg.DeliveryMode = Foreground
	}
	if cfg.DeliveryMode != Foreground {
		return nil, refuse("invalid_config", "only foreground desktop input is supported")
	}
	if cfg.CaptureTTL == 0 {
		cfg.CaptureTTL = 30 * time.Second
	}
	if cfg.CaptureTTL < 0 || cfg.CaptureTTL > 30*time.Second {
		return nil, refuse("invalid_config", "CaptureTTL must be positive and at most 30 seconds")
	}
	runner := cfg.Runner
	if runner == nil {
		runner = commandRunner{}
	}
	started := cfg.ProcessStartedAt
	if started == nil {
		started = func(ctx context.Context, pid int) (time.Time, error) {
			if err := ctx.Err(); err != nil {
				return time.Time{}, err
			}
			return process.StartTime(pid)
		}
	}
	id := uuid.NewString()
	root := filepath.Join(cfg.DataDir, "testing", "cua", id)
	controlRoot := root
	if len(filepath.Join(controlRoot, "driver.sock")) > 103 {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		controlRoot = filepath.Join(home, ".ao", "dev", "cua", id)
		if len(filepath.Join(controlRoot, "driver.sock")) > 103 {
			return nil, refuse("invalid_config", "private Unix socket path exceeds the macOS limit")
		}
	}
	return &Adapter{cfg: cfg, runner: runner, started: started, root: root, controlRoot: controlRoot,
		bindings: make(map[string]*binding), released: make(map[string]*binding), now: time.Now, startRecorder: startScreencapture,
		interruptWait: 15 * time.Second, terminateWait: 3 * time.Second}, nil
}

// DeliveryMode lets the service journal the configured policy before dispatch.
func (a *Adapter) DeliveryMode() DeliveryMode { return a.cfg.DeliveryMode }

// BindWindow pins a single layer-zero window of the exact live Electron PID.
// Ambiguous windows require the daemon to supply a specific WindowID; there is
// no largest-window, frontmost-app, or desktop fallback.
func (a *Adapter) BindWindow(ctx context.Context, target domain.TestTargetIdentity) (domain.TestTargetIdentity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return target, refuse("closed", "adapter has been closed")
	}
	if target.ID == "" || target.LaunchID == "" || target.Generation <= 0 || target.ElectronPID <= 0 || target.ElectronStartedAt.IsZero() {
		return target, refuse("invalid_target", "target launch identity and Electron birth time are required")
	}
	if existing := a.bindings[target.ID]; existing != nil {
		candidate := target
		candidate.WindowID = existing.target.WindowID
		if !sameTarget(candidate, existing.target) || (target.WindowID != "" && target.WindowID != candidate.WindowID) {
			return target, refuse("target_changed", "a target ID cannot be rebound to a different launch or window")
		}
		if !existing.ready {
			return target, refuse("binding_pending", "partial binding must be released before another bind")
		}
		if err := a.startSession(ctx, existing); err != nil {
			return target, err
		}
		if _, err := a.liveWindow(ctx, existing); err != nil {
			return target, err
		}
		return existing.target, nil
	}
	if prior := a.released[target.ID]; prior != nil && sameTarget(prior.target, target) {
		return target, refuse("target_released", "released launch identity cannot be bound again")
	}
	b := &binding{target: target, session: "ao-" + uuid.NewString()}
	a.bindings[target.ID] = b // cleanup ownership only; observation requires ready
	if err := a.checkProcess(ctx, target); err != nil {
		return target, err
	}
	if err := a.checkScreenUnlocked(ctx); err != nil {
		return target, err
	}
	if err := a.ensureDriver(ctx); err != nil {
		return target, err
	}
	if err := a.startSession(ctx, b); err != nil {
		return target, err
	}
	windows, err := a.windows(ctx, b)
	if err != nil {
		return target, err
	}
	var candidates []window
	for _, w := range windows {
		if w.Layer == 0 && validBounds(w.Bounds) && (target.WindowID == "" || target.WindowID == strconv.Itoa(w.ID)) {
			candidates = append(candidates, w)
		}
	}
	if len(candidates) != 1 {
		return target, refuse("window_ambiguous", "expected exactly one usable window owned by the target PID")
	}
	if err := a.checkProcess(ctx, target); err != nil {
		return target, err
	}
	b.target.WindowID = strconv.Itoa(candidates[0].ID)
	b.ready = true
	delete(a.released, target.ID)
	return b.target, nil
}

// Screenshot captures only the bound window and returns a preview capped at
// 1568 pixels on its long edge, retaining original PNG bytes for evidence. The
// service must preserve Frame, including CaptureHandle, when assigning the
// evidence ScreenshotID. No provider identifier or provider path is returned.
func (a *Adapter) Screenshot(ctx context.Context, target domain.TestTargetIdentity) (shot domain.TestScreenshot, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.screenshot(ctx, target)
}

func (a *Adapter) screenshot(ctx context.Context, target domain.TestTargetIdentity) (shot domain.TestScreenshot, err error) {
	b, err := a.bound(ctx, target)
	if err != nil {
		return shot, err
	}
	b.receipt = nil
	if err := a.startSession(ctx, b); err != nil {
		return shot, err
	}
	if _, err = a.prepareWindow(ctx, b); err != nil {
		return shot, err
	}
	f, err := os.CreateTemp(filepath.Join(a.root, "captures"), "frame-*.png")
	if err != nil {
		return shot, fmt.Errorf("create capture file: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return shot, err
	}
	defer func() { err = errors.Join(err, os.Remove(path)) }()
	args := a.targetArgs(b)
	args["max_image_dimension"] = 0
	args["include_accessibility_tree"] = true
	args["max_elements"] = 300
	args["timeout_ms"] = 1000
	args["screenshot_out_file"] = path
	capturedAt := a.now().UTC() // conservative age includes the capture call
	var state captureState
	if err := a.call(ctx, "get_window_state", args, &state); err != nil {
		return shot, err
	}
	if state.PID != target.ElectronPID || strconv.Itoa(state.WindowID) != target.WindowID || state.Capture == "" || !validBounds(state.Bounds) {
		return shot, refuse("capture_target_mismatch", "capture did not prove the bound PID, window and frame")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 || info.Size() > 64<<20 {
		return shot, refuse("capture_invalid", "capture file missing, empty or larger than 64 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return shot, err
	}
	dimensions, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || dimensions.Width != state.Width || dimensions.Height != state.Height || !coherentScale(state) {
		return shot, refuse("capture_invalid", "PNG dimensions and native point scale disagree")
	}
	w, err := a.liveWindow(ctx, b)
	if err != nil {
		return shot, err
	}
	if w.Bounds != state.Bounds {
		return shot, refuse("window_changed", "window moved or resized during capture")
	}
	frame := domain.TestDesktopFrame{Target: target, Bounds: state.Bounds, Width: state.Width, Height: state.Height,
		Scale: state.Scale, CaptureHandle: uuid.NewString(), CapturedAt: capturedAt}
	preview, width, height, err := screenshotPreview(data, state.Width, state.Height)
	if err != nil {
		return shot, fmt.Errorf("resize captured PNG: %w", err)
	}
	shot = domain.TestScreenshot{Frame: frame, MIMEType: "image/png", Data: preview, InputReady: true}
	if width != state.Width || height != state.Height {
		shot.Original = &domain.TestScreenshot{Frame: frame, MIMEType: "image/png", Data: data}
		shot.Frame.Width, shot.Frame.Height = width, height
	}
	b.receipt = &captureReceipt{frame: shot.Frame, providerID: state.Capture, elements: state.Elements, width: state.Width, height: state.Height}
	shot.Elements, shot.Truncated = b.receipt.observationElements(state)
	if b.lastTyped != nil {
		if b.lastTyped.bounds != frame.Bounds {
			b.lastTyped = nil
		} else {
			b.receipt.keyToken = typedFieldToken(state.Elements, b.lastTyped.point)
		}
	}
	return shot, nil
}

func boundedText(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	return string([]rune(text)[:limit]), true
}

func (r *captureReceipt) observationElements(state captureState) ([]domain.TestElement, bool) {
	elements := []domain.TestElement{}
	r.addressable = map[string]capturedElement{}
	truncated := state.Truncated || len(state.Elements) >= 300 || (state.ElementsComplete != nil && !*state.ElementsComplete)
	for _, element := range state.Elements {
		f := element.Frame
		if element.Token == "" || element.Role == "" || f == nil || (element.Visible != nil && !*element.Visible) ||
			!validBounds(domain.TestWindowBounds{X: f.X, Y: f.Y, Width: f.Width, Height: f.Height}) {
			continue
		}
		x, y := math.Max(0, f.X), math.Max(0, f.Y)
		right, bottom := math.Min(float64(r.width), f.X+f.Width), math.Min(float64(r.height), f.Y+f.Height)
		if right <= x || bottom <= y {
			continue
		}
		if len(elements) == 300 {
			truncated = true
			break
		}
		label := element.Label
		if label == "" {
			label = element.Title
		}
		value := ""
		if element.Role == "AXTextField" || element.Role == "AXTextArea" {
			_ = json.Unmarshal(element.Value, &value)
		}
		role, cutRole := boundedText(element.Role, 64)
		label, cutLabel := boundedText(label, 256)
		value, cutValue := boundedText(value, 256)
		truncated = truncated || cutRole || cutLabel || cutValue
		id := uuid.NewString()
		mapX := func(value float64) float64 { return value / float64(r.width) * float64(r.frame.Width) }
		mapY := func(value float64) float64 { return value / float64(r.height) * float64(r.frame.Height) }
		left, top := mapX(x), mapY(y)
		elements = append(elements, domain.TestElement{ElementID: id, Role: role, Label: label, Value: value,
			Enabled: element.Enabled, Focused: element.Focused,
			Frame: domain.TestWindowBounds{X: left, Y: top, Width: mapX(right) - left, Height: mapY(bottom) - top}})
		// Resolve only the visible portion, never a center outside this window.
		element.Frame = &pixelBounds{X: x, Y: y, Width: right - x, Height: bottom - y}
		r.addressable[id] = element
	}
	return elements, truncated
}

func screenshotPreview(data []byte, width, height int) ([]byte, int, int, error) {
	const maxDimension = 1568
	if max(width, height) <= maxDimension {
		return data, width, height, nil
	}
	ratio := float64(maxDimension) / float64(max(width, height))
	w, h := max(1, int(math.Round(float64(width)*ratio))), max(1, int(math.Round(float64(height)*ratio)))
	source, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	preview := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(preview, preview.Bounds(), source, source.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, preview); err != nil {
		return nil, 0, 0, err
	}
	return encoded.Bytes(), w, h, nil
}

// Map the full returned pixel range onto the full provider pixel range. Use
// each rounded preview dimension separately, and leave Retina conversion to
// Cua. Both first and last pixels map exactly to the original image's edges.
func (r *captureReceipt) originalPoint(p pixel) pixel {
	mapAxis := func(value, returned, original int) int {
		if returned <= 1 {
			return 0
		}
		return int(math.Round(float64(value) * float64(original-1) / float64(returned-1)))
	}
	return pixel{mapAxis(p.x, r.frame.Width, r.width), mapAxis(p.y, r.frame.Height, r.height)}
}

func coherentScale(s captureState) bool {
	return (s.Scale == 1 || s.Scale == 2) && s.Width > 0 && s.Height > 0 &&
		math.Abs(float64(s.Width)-s.Bounds.Width*s.Scale) <= 1 && math.Abs(float64(s.Height)-s.Bounds.Height*s.Scale) <= 1
}

// Click admits one fresh screenshot. Pixel clicks also send its Cua capture ID.
func (a *Adapter) Click(ctx context.Context, target domain.TestTargetIdentity, frame domain.TestDesktopFrame, request domain.TestClickRequest) (domain.TestActionResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if request.Button != "" && request.Button != domain.TestMouseButtonLeft && request.Button != domain.TestMouseButtonRight && request.Button != domain.TestMouseButtonMiddle {
		return domain.TestActionResult{}, refuse("invalid_button", "unsupported mouse button")
	}
	p := &pixel{request.X, request.Y}
	if request.ElementID != "" {
		p = nil
	}
	b, receipt, err := a.admit(ctx, target, frame, request.ScreenshotID, p, request.ElementID)
	if err != nil {
		return domain.TestActionResult{}, err
	}
	args := a.inputArgs(b)
	point := receipt.originalPoint(pixel{request.X, request.Y})
	if request.ElementID != "" {
		element := receipt.addressable[request.ElementID]
		if request.Button == domain.TestMouseButtonMiddle {
			f := element.Frame
			args["x"], args["y"] = int(f.X+f.Width/2), int(f.Y+f.Height/2)
		} else {
			args["element_token"] = element.Token
		}
	} else {
		args["x"], args["y"] = point.x, point.y
	}
	if args["x"] != nil && args["y"] != nil {
		args["capture_id"] = receipt.providerID
	}
	if request.Button != "" {
		args["button"] = string(request.Button)
	}
	b.lastTyped = nil
	return a.dispatch(ctx, "click", args)
}

// Type preserves pixel focus for coordinates and uses AX for an element ID.
func (a *Adapter) Type(ctx context.Context, target domain.TestTargetIdentity, frame domain.TestDesktopFrame, request domain.TestTypeRequest) (domain.TestActionResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := &pixel{request.X, request.Y}
	if request.ElementID != "" {
		p = nil
	}
	b, receipt, err := a.admit(ctx, target, frame, request.ScreenshotID, p, request.ElementID)
	if err != nil {
		return domain.TestActionResult{}, err
	}
	args := a.inputArgs(b)
	point := receipt.originalPoint(pixel{request.X, request.Y})
	args["x"], args["y"], args["text"] = point.x, point.y, request.Text
	b.lastTyped = nil
	if request.ElementID != "" {
		element := receipt.addressable[request.ElementID]
		delete(args, "x")
		delete(args, "y")
		args["element_token"] = element.Token
		f := element.Frame
		point = pixel{int(f.X + f.Width/2), int(f.Y + f.Height/2)}
	}
	// Remember the addressed field, including uncertain partial delivery. A
	// subsequent key still requires a new screenshot and a fresh field token.
	b.lastTyped = &typedFocus{point: point, bounds: frame.Bounds}
	return a.dispatch(ctx, "type_text", args)
}

// Key sends only a validated simultaneous chord to the bound window.
func (a *Adapter) Key(ctx context.Context, target domain.TestTargetIdentity, frame domain.TestDesktopFrame, request domain.TestKeyRequest) (domain.TestActionResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key, modifiers, err := keyChord(request.Keys)
	if err != nil {
		return domain.TestActionResult{}, err
	}
	b, receipt, err := a.admit(ctx, target, frame, request.ScreenshotID, nil, "")
	if err != nil {
		return domain.TestActionResult{}, err
	}
	args := a.inputArgs(b)
	args["key"], args["modifiers"] = key, modifiers
	if a.cfg.DeliveryMode == Foreground && b.lastTyped != nil {
		if receipt.keyToken == "" {
			return domain.TestActionResult{}, refuse("key_focus_unresolved", "fresh snapshot cannot resolve the last typed field in this window")
		}
		// Electron reactivation can clear renderer focus. Re-focus only the
		// exact field at the prior Type point using this fresh window snapshot.
		args["element_token"] = receipt.keyToken
	}
	return a.dispatch(ctx, "press_key", args)
}

func typedFieldToken(elements []capturedElement, point pixel) string {
	token := ""
	for _, element := range elements {
		if (element.Role != "AXTextField" && element.Role != "AXTextArea") || element.Frame == nil || element.Token == "" {
			continue
		}
		frame := element.Frame
		if float64(point.x) >= frame.X && float64(point.x) < frame.X+frame.Width && float64(point.y) >= frame.Y && float64(point.y) < frame.Y+frame.Height {
			if token != "" {
				return ""
			}
			token = element.Token
		}
	}
	return token
}

type pixel struct{ x, y int }

func (a *Adapter) admit(ctx context.Context, target domain.TestTargetIdentity, frame domain.TestDesktopFrame, screenshotID string, p *pixel, elementID string) (*binding, *captureReceipt, error) {
	if a.cfg.DeliveryMode != Foreground {
		return nil, nil, refuse("foreground_required", "background input did not register in the owned Electron window")
	}
	b, err := a.bound(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	r := b.receipt
	if screenshotID == "" || frame.ScreenshotID != screenshotID || r == nil || frame.CaptureHandle == "" || frame.CaptureHandle != r.frame.CaptureHandle ||
		!sameTarget(frame.Target, target) || frame.Bounds != r.frame.Bounds || frame.Width != r.frame.Width || frame.Height != r.frame.Height ||
		frame.Scale != r.frame.Scale || !frame.CapturedAt.Equal(r.frame.CapturedAt) || (r.screenshotID != "" && r.screenshotID != screenshotID) {
		return nil, nil, refuse("screenshot_mismatch", "input requires this window's current evidence screenshot receipt")
	}
	age := a.now().Sub(frame.CapturedAt)
	if age < 0 || age >= a.cfg.CaptureTTL {
		return nil, nil, refuse("screenshot_stale", "capture expired; take a new screenshot")
	}
	if p != nil && (p.x < 0 || p.y < 0 || p.x >= frame.Width || p.y >= frame.Height) {
		return nil, nil, refuse("coordinate_outside_window", "point lies outside returned screenshot pixels")
	}
	if elementID != "" {
		element, ok := r.addressable[elementID]
		if !ok || (element.Enabled != nil && !*element.Enabled) {
			return nil, nil, refuse("element_mismatch", "element is foreign, stale or disabled; observe again")
		}
	}
	r.screenshotID = screenshotID
	w, err := a.liveWindow(ctx, b)
	if err != nil {
		return nil, nil, err
	}
	if w.Bounds != frame.Bounds {
		return nil, nil, refuse("window_changed", "window moved or resized; take a new screenshot")
	}
	w, err = a.prepareWindow(ctx, b)
	if err != nil {
		return nil, nil, err
	}
	if w.Bounds != frame.Bounds {
		return nil, nil, refuse("window_changed", "window moved during foreground preparation; take a new screenshot")
	}
	if p != nil || elementID != "" {
		var point pixel
		if p != nil {
			point = r.originalPoint(*p)
		} else {
			f := r.addressable[elementID].Frame
			point = pixel{int(f.X + f.Width/2), int(f.Y + f.Height/2)}
		}
		if err := a.checkClickPoint(ctx, b, r, point); err != nil {
			return nil, nil, err
		}
	}
	if age := a.now().Sub(frame.CapturedAt); age < 0 || age >= a.cfg.CaptureTTL {
		return nil, nil, refuse("screenshot_stale", "capture expired during validation; take a new screenshot")
	}
	// A dispatched or uncertain input consumes the receipt, including type/key
	// whose Cua schemas lack capture_id. No automatic retry can duplicate input.
	b.receipt = nil
	return b, r, nil
}

func (a *Adapter) dispatch(ctx context.Context, name string, args map[string]any) (domain.TestActionResult, error) {
	var result struct {
		Effect  string `json:"effect"`
		Summary string `json:"summary"`
	}
	if err := a.call(ctx, name, args, &result); err != nil {
		return domain.TestActionResult{}, err
	}
	if (result.Effect != "confirmed" && result.Effect != "unverifiable") || result.Summary == "" {
		return domain.TestActionResult{}, &Error{Code: "provider_protocol", Detail: "input result lacks an explicit delivery effect", cause: ErrProvider}
	}
	inputPath := "keyboard"
	if name == "click" {
		inputPath = "pointer"
		if args["element_token"] != nil {
			inputPath = "ax"
		}
	} else if name == "type_text" {
		inputPath = "pointer+keyboard"
		if args["element_token"] != nil {
			inputPath = "ax+keyboard"
		}
	} else if args["element_token"] != nil {
		inputPath = "ax+keyboard"
	}
	return domain.TestActionResult{Delivered: true, InputPath: "unknown", RequestedInputPath: inputPath, Detail: fmt.Sprintf("delivery_mode=%s; %s", args["delivery_mode"], result.Summary)}, nil
}

func (a *Adapter) bound(ctx context.Context, target domain.TestTargetIdentity) (*binding, error) {
	b := a.bindings[target.ID]
	if a.closed || b == nil || !b.ready || !sameTarget(b.target, target) {
		return nil, refuse("target_not_bound", "target does not match a live adapter binding")
	}
	if err := a.checkProcess(ctx, target); err != nil {
		return nil, err
	}
	if err := a.checkDriver(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

func (a *Adapter) checkProcess(ctx context.Context, target domain.TestTargetIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	actual, err := a.started(ctx, target.ElectronPID)
	if err != nil || !actual.Equal(target.ElectronStartedAt) {
		return refuse("process_changed", "Electron PID is missing or its birth timestamp changed")
	}
	return nil
}

func sameTarget(x, y domain.TestTargetIdentity) bool {
	return x.ID == y.ID && x.LaunchID == y.LaunchID && x.Generation == y.Generation && x.ElectronPID == y.ElectronPID &&
		x.ElectronStartedAt.Equal(y.ElectronStartedAt) && x.DaemonPID == y.DaemonPID && x.DaemonStartedAt.Equal(y.DaemonStartedAt) &&
		x.DataDir == y.DataDir && x.WindowID == y.WindowID
}

func validBounds(b domain.TestWindowBounds) bool {
	return !math.IsNaN(b.X) && !math.IsNaN(b.Y) && !math.IsInf(b.X, 0) && !math.IsInf(b.Y, 0) &&
		b.Width > 0 && b.Height > 0 && !math.IsInf(b.Width, 0) && !math.IsInf(b.Height, 0)
}

type window struct {
	PID            int                     `json:"pid"`
	ID             int                     `json:"window_id"`
	Layer          int                     `json:"layer"`
	Bounds         domain.TestWindowBounds `json:"bounds"`
	OnScreen       bool                    `json:"is_on_screen"`
	OnCurrentSpace *bool                   `json:"on_current_space"`
	CurrentSpaceID uint64                  `json:"current_space_id"`
}

func (a *Adapter) windows(ctx context.Context, b *binding) ([]window, error) {
	var result struct {
		Windows []window `json:"windows"`
	}
	args := a.targetArgs(b)
	// list_windows does not accept window_id. PID is always injected.
	delete(args, "window_id")
	if err := a.call(ctx, "list_windows", args, &result); err != nil {
		return nil, err
	}
	for _, w := range result.Windows {
		if w.PID != b.target.ElectronPID || w.ID <= 0 {
			return nil, refuse("window_owner_mismatch", "provider returned a foreign or invalid window")
		}
	}
	return result.Windows, nil
}

func (a *Adapter) liveWindow(ctx context.Context, b *binding) (window, error) {
	if err := a.checkProcess(ctx, b.target); err != nil {
		return window{}, err
	}
	windows, err := a.windows(ctx, b)
	if err != nil {
		return window{}, err
	}
	for _, w := range windows {
		if strconv.Itoa(w.ID) == b.target.WindowID && w.Layer == 0 && validBounds(w.Bounds) {
			if err := a.checkProcess(ctx, b.target); err != nil {
				return window{}, err
			}
			return w, nil
		}
	}
	return window{}, refuse("window_missing", "bound window no longer exists")
}

func (a *Adapter) targetArgs(b *binding) map[string]any {
	args := map[string]any{"pid": b.target.ElectronPID, "session": b.session}
	if b.target.WindowID != "" {
		id, _ := strconv.Atoi(b.target.WindowID) // IDs originate in validated provider integers.
		args["window_id"] = id
	}
	return args
}

func (a *Adapter) inputArgs(b *binding) map[string]any {
	args := a.targetArgs(b)
	args["scope"], args["delivery_mode"] = "window", string(a.cfg.DeliveryMode)
	return args
}

func (a *Adapter) call(ctx context.Context, name string, args map[string]any, result any) error {
	if err := a.checkDriver(ctx); err != nil {
		return fmt.Errorf("cua call %s: %w", name, errors.Join(err, ctx.Err()))
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("cua call %s encode arguments: %w", name, err)
	}
	out, runErr := a.run(ctx, a.binary(), "--socket", a.socket(), "call", name, string(encoded))
	var status struct {
		Code    string          `json:"code"`
		Effect  string          `json:"effect"`
		Summary string          `json:"summary"`
		Message string          `json:"message"`
		IsError bool            `json:"isError"`
		Reason  json.RawMessage `json:"reason"`
		Error   json.RawMessage `json:"error"`
	}
	decodeErr := json.Unmarshal(out.Stdout, &status)
	if runErr != nil || decodeErr != nil || status.IsError || status.Effect == "refused" {
		code := status.Code
		if code == "" {
			code = "provider_failure"
		}
		detail := status.Summary
		if strings.TrimSpace(detail) == "" {
			detail = status.Message
		}
		for _, raw := range []json.RawMessage{status.Reason, status.Error} {
			if strings.TrimSpace(detail) == "" {
				detail = providerDiagnosticCause(raw, 0)
			}
		}
		if strings.TrimSpace(detail) == "" {
			detail = string(out.Stderr)
		}
		if strings.TrimSpace(detail) == "" && !json.Valid(out.Stdout) {
			detail = string(out.Stdout)
		}
		code = providerDiagnosticText(code, args, 64)
		if !providerDiagnosticCode.MatchString(code) {
			code = "provider_failure"
		}
		return &Error{Code: code, Detail: providerCallDiagnostic(ctx, name, args, detail, runErr, decodeErr), cause: errors.Join(ErrProvider, runErr, decodeErr, ctx.Err())}
	}
	if result != nil {
		if err := json.Unmarshal(out.Stdout, result); err != nil {
			return &Error{Code: "provider_protocol", Detail: providerCallDiagnostic(ctx, name, args, "invalid structured result", nil, err), cause: errors.Join(ErrProvider, err, ctx.Err())}
		}
	}
	return nil
}

const providerDiagnosticLimit = 1024

var (
	providerDiagnosticCode    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	providerDiagnosticPrivate = regexp.MustCompile(`(?i)\b(?:[a-z0-9_]*(?:token|password|secret|api[_-]?key|capability)|authorization|credentials?|cookies?|prompt|text|screenshot(?:_data)?|image(?:_data)?|base64|data|payload)\b["']?\s*[:=]\s*(?:"(?:\\.|[^"])*(?:"|$)|'(?:\\.|[^'])*(?:'|$)|[^\r\n]*)|\b(?:Bearer|Basic)\s+[^\s,;]+|data:image/[^\s]+|https?://[^\s/@]+:[^\s/@]+@[^\s]+|[A-Za-z0-9+/=_-]{43,}`)
)

// Only diagnostic fields are read from structured causes. Provider result and
// argument objects can contain captures, typed text and credentials.
func providerDiagnosticCause(raw json.RawMessage, depth int) string {
	if depth > 2 || len(raw) == 0 || len(raw) > 8*providerDiagnosticLimit {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var cause struct {
		Summary string          `json:"summary"`
		Message string          `json:"message"`
		Reason  json.RawMessage `json:"reason"`
		Error   json.RawMessage `json:"error"`
		Cause   json.RawMessage `json:"cause"`
	}
	if json.Unmarshal(raw, &cause) != nil {
		return ""
	}
	for _, text := range []string{cause.Summary, cause.Message} {
		if strings.TrimSpace(text) != "" {
			return text
		}
	}
	for _, nested := range []json.RawMessage{cause.Reason, cause.Error, cause.Cause} {
		if text := providerDiagnosticCause(nested, depth+1); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func providerCallDiagnostic(ctx context.Context, name string, args map[string]any, detail string, runErr, decodeErr error) string {
	parts := []string{"call " + providerDiagnosticText(name, nil, 64)}
	if runErr != nil {
		parts = append(parts, "run: "+providerDiagnosticText(runErr.Error(), args, 128))
	}
	if decodeErr != nil {
		parts = append(parts, "decode: "+providerDiagnosticText(decodeErr.Error(), args, 128))
	}
	if err := ctx.Err(); err != nil {
		parts = append(parts, "context: "+err.Error())
	}
	if strings.TrimSpace(detail) == "" {
		detail = "no recognized provider diagnostic"
	}
	parts = append(parts, "cause: "+providerDiagnosticText(detail, args, 512))
	return strings.Join(parts, "; ")
}

func providerDiagnosticText(text string, args map[string]any, limit int) string {
	values := make([]string, 0, len(args))
	for key, value := range args {
		if key == "scope" || key == "delivery_mode" || key == "button" {
			continue
		}
		if value, ok := value.(string); ok && value != "" {
			values = append(values, value)
		}
	}
	// A session or capture token can be embedded in typed text. Redacting the
	// longest value first keeps that replacement independent of map order.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	if len(text) > 8*providerDiagnosticLimit {
		text = text[:8*providerDiagnosticLimit]
		for !utf8.ValidString(text) && len(text) > 8*providerDiagnosticLimit-utf8.UTFMax {
			text = text[:len(text)-1]
		}
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return "[non-text output omitted]"
	}
	text = providerDiagnosticPrivate.ReplaceAllString(text, "[redacted]")
	text = strings.Map(func(r rune) rune {
		if r < ' ' || r == '\x7f' {
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > limit {
		text = text[:limit-len(" [truncated]")]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += " [truncated]"
	}
	return text
}
