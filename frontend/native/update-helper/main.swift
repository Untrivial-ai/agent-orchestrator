import AppKit
import Darwin
import CoreGraphics

func processAlive(_ pid: Int32) -> Bool {
    kill(pid, 0) == 0 || errno == EPERM
}

func readJSON<T: Decodable>(_ type: T.Type, at url: URL) -> T? {
    guard let data = try? Data(contentsOf: url), data.count <= 16_384 else { return nil }
    return try? JSONDecoder().decode(type, from: data)
}

final class ProgressController: NSObject, NSApplicationDelegate, NSWindowDelegate {
    let attempt: URL
    let request: UpdateRequest
    var state: UpdateProgressState
    var window: NSWindow!
    var timer: Timer?
    var displayedStage: UpdateStage?
    var windowPresented = false
    let title = NSTextField(labelWithString: "")
    let detail = NSTextField(wrappingLabelWithString: "")
    let spinner = NSProgressIndicator()
    let recovery = NSStackView()

    init(attempt: URL, request: UpdateRequest) {
        self.attempt = attempt
        self.request = request
        self.state = UpdateProgressState(request: request, now: Date().timeIntervalSince1970 * 1000)
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Start as an accessory: no Dock icon and no window on launch. A normal
        // update just closes AO and reopens it, with no second window in the way.
        NSApp.setActivationPolicy(.accessory)
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 500, height: 270),
                          styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = "Updating Agent Orchestrator"
        window.isReleasedWhenClosed = false
        window.delegate = self
        title.font = .systemFont(ofSize: 22, weight: .semibold)
        detail.font = .systemFont(ofSize: 14)
        detail.textColor = .secondaryLabelColor
        detail.preferredMaxLayoutWidth = 430
        spinner.style = .spinning
        spinner.controlSize = .regular
        spinner.isIndeterminate = true
        let version = NSTextField(labelWithString: "Updating to \(request.version)")
        version.font = .systemFont(ofSize: 12)
        version.textColor = .secondaryLabelColor
        recovery.orientation = .horizontal
        recovery.spacing = 10
        recovery.addArrangedSubview(NSButton(title: "Keep Waiting", target: self, action: #selector(keepWaiting)))
        recovery.addArrangedSubview(NSButton(title: "Download Latest App", target: self, action: #selector(download)))
        recovery.addArrangedSubview(NSButton(title: "Close", target: self, action: #selector(closeWindow)))
        let stack = NSStackView(views: [spinner, title, detail, version, recovery])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 16
        stack.translatesAutoresizingMaskIntoConstraints = false
        window.contentView!.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: window.contentView!.leadingAnchor, constant: 32),
            stack.trailingAnchor.constraint(equalTo: window.contentView!.trailingAnchor, constant: -32),
            stack.centerYAnchor.constraint(equalTo: window.contentView!.centerYAnchor),
        ])
        refresh()
        // READY is what the parent waits for before letting Squirrel quit AO.
        // It is the process handshake (this stdout line), not any visible UI, so
        // the window can stay hidden here and still hand off safely. The window
        // is shown only if the update stalls or fails (see presentWindow).
        DispatchQueue.main.async {
            FileHandle.standardOutput.write(Data("READY\n".utf8))
        }
        timer = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in self?.refresh() }
    }

    func legacyEvidence(now: Double) -> LegacyLaunchEvidence? {
        guard !processAlive(request.parentPID) else { return nil }
        let markerURL = attempt.deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("app-state.json")
        guard let marker = readJSON(LegacyAppMarker.self, at: markerURL), marker.matchesLegacyLaunch(request, now: now) else { return nil }
        guard let app = NSWorkspace.shared.runningApplications.first(where: {
            $0.processIdentifier != request.parentPID && !$0.isTerminated && $0.isFinishedLaunching &&
            $0.bundleURL?.standardizedFileURL.path == request.appPath
        }) else { return nil }
        // Public window metadata only. Do not read window titles, capture pixels,
        // request Screen Recording/Accessibility permission, or activate the app.
        let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]]
        let visible = windows?.contains(where: { info in
            guard (info[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == app.processIdentifier,
                  (info[kCGWindowLayer as String] as? NSNumber)?.intValue == 0,
                  (info[kCGWindowAlpha as String] as? NSNumber)?.doubleValue ?? 0 > 0,
                  let bounds = info[kCGWindowBounds as String] as? [String: Any],
                  (bounds["Width"] as? NSNumber)?.doubleValue ?? 0 >= 100,
                  (bounds["Height"] as? NSNumber)?.doubleValue ?? 0 >= 80 else { return false }
            return true
        }) ?? false
        return LegacyLaunchEvidence(marker: marker, pid: app.processIdentifier,
                                    appPath: app.bundleURL?.standardizedFileURL.path,
                                    finishedLaunching: app.isFinishedLaunching, visibleWindow: visible)
    }

    // Bring the window on screen. Called only when the update needs attention
    // (it stalled or failed), never on the normal close-and-reopen path.
    func presentWindow() {
        guard !windowPresented else { return }
        windowPresented = true
        NSApp.setActivationPolicy(.regular)
        window.center()
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    func refresh() {
        let completion = readJSON(UpdateCompletion.self, at: attempt.appendingPathComponent("complete.json"))
        let failure = readJSON(UpdateFailure.self, at: attempt.appendingPathComponent("error.json"))
        let now = Date().timeIntervalSince1970 * 1000
        let next = state.stage(now: now,
                               parentAlive: processAlive(request.parentPID), completion: completion, failure: failure?.message, legacy: legacyEvidence(now: now))
        if next == .complete || next == .legacyComplete {
            cleanup()
            NSApp.terminate(nil)
            return
        }
        guard next != displayedStage else { return }
        displayedStage = next
        recovery.arrangedSubviews.forEach { $0.isHidden = false }
        switch next {
        case .closing:
            title.stringValue = "Closing AO"
            detail.stringValue = "Preparing to install your update. This window will stay open while AO restarts."
            recovery.isHidden = true
            spinner.startAnimation(nil)
        case .installing:
            title.stringValue = "Installing and reopening AO"
            detail.stringValue = "macOS is installing the update. AO will reopen automatically when it is ready."
            recovery.isHidden = true
            spinner.startAnimation(nil)
        case .recovery(let message):
            presentWindow()
            window.setContentSize(NSSize(width: 500, height: 340))
            title.stringValue = "Still waiting for AO"
            detail.stringValue = String(message.prefix(260))
            recovery.isHidden = false
            spinner.stopAnimation(nil)
        case .reopened:
            presentWindow()
            title.stringValue = "AO has reopened"
            detail.stringValue = "This version of AO cannot confirm when its window is ready. You can close this progress window."
            recovery.isHidden = false
            recovery.arrangedSubviews.prefix(2).forEach { $0.isHidden = true }
            spinner.stopAnimation(nil)
        case .complete, .legacyComplete: break
        }
    }

    @objc func keepWaiting() {
        state.keepWaiting(now: Date().timeIntervalSince1970 * 1000)
        refresh()
    }
    @objc func download() {
        NSWorkspace.shared.open(URL(string: "https://github.com/Untrivial-ai/agent-orchestrator/releases/latest")!)
    }
    @objc func closeWindow() { window.performClose(nil) }
    func windowWillClose(_ notification: Notification) {
        if !processAlive(request.parentPID) { cleanup() }
        NSApp.terminate(nil)
    }
    func cleanup() {
        timer?.invalidate()
        // The executable and its data have a dedicated directory. Never touch active.json:
        // a newer attempt may already own that pointer.
        try? FileManager.default.removeItem(at: attempt)
    }
}

// Only a copied helper, outside any .app, may run. Refuse an arbitrary cleanup path.
guard CommandLine.arguments.count == 2 else { exit(2) }
let attempt = URL(fileURLWithPath: CommandLine.arguments[1]).standardizedFileURL
let executable = URL(fileURLWithPath: CommandLine.arguments[0]).standardizedFileURL.resolvingSymlinksInPath()
guard attempt == attempt.resolvingSymlinksInPath(),
      attempt.deletingLastPathComponent().lastPathComponent == "update-restart",
      attempt.lastPathComponent.hasPrefix("attempt-"),
      !attempt.pathComponents.contains(where: { $0.hasSuffix(".app") }),
      executable == attempt.appendingPathComponent("ao-update-progress"),
      let request = readJSON(UpdateRequest.self, at: attempt.appendingPathComponent("request.json")), request.valid else { exit(2) }
let controller = ProgressController(attempt: attempt, request: request)
NSApplication.shared.delegate = controller
NSApplication.shared.run()
