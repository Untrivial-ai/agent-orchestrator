import Foundation

struct UpdateRequest: Codable, Equatable {
    let version: String
    let appPath: String
    let parentPID: Int32
    let startedAt: Double

    var valid: Bool {
        !version.isEmpty && appPath.hasPrefix("/") && appPath.hasSuffix(".app") &&
        parentPID > 0 && startedAt.isFinite && startedAt > 0
    }
}

struct UpdateCompletion: Codable {
    let version: String
    let appPath: String
    let parentPID: Int32
    let startedAt: Double
    let pid: Int32

    func matches(_ request: UpdateRequest) -> Bool {
        version == request.version && appPath == request.appPath &&
        parentPID == request.parentPID && startedAt == request.startedAt &&
        pid > 0 && pid != request.parentPID
    }
}

struct UpdateFailure: Codable { let message: String }

struct LegacyAppMarker: Decodable {
    let appPath: String
    let version: String
    let lastReconciledAt: String
    let updateRestartProtocol: Int?

    func matchesLegacyLaunch(_ request: UpdateRequest, now: Double) -> Bool {
        // Only old clients lack the explicit acknowledgement capability. Never weaken
        // readiness for new clients, even if their window appears before their shell.
        guard updateRestartProtocol == nil, appPath == request.appPath, version == request.version else { return false }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        var date = formatter.date(from: lastReconciledAt)
        if date == nil {
            formatter.formatOptions = [.withInternetDateTime]
            date = formatter.date(from: lastReconciledAt)
        }
        guard let date else { return false }
        let timestamp = date.timeIntervalSince1970 * 1000
        return timestamp >= request.startedAt && timestamp <= now
    }
}

struct LegacyLaunchEvidence {
    let marker: LegacyAppMarker?
    let pid: Int32?
    let appPath: String?
    let finishedLaunching: Bool
    let visibleWindow: Bool

    func launched(_ request: UpdateRequest, now: Double) -> Bool {
        guard let marker, marker.matchesLegacyLaunch(request, now: now),
              let pid, pid > 0, pid != request.parentPID,
              appPath == request.appPath, finishedLaunching else { return false }
        return true
    }
}

enum UpdateStage: Equatable {
    case closing
    case installing
    case recovery(String)
    case complete
    case reopened
    case legacyComplete

    // Whether this stage should bring the helper window on screen. The normal
    // close-and-reopen path (.closing/.installing) stays silent; only the
    // stall/failure paths that need the user's attention surface a window.
    var presentsWindow: Bool {
        switch self {
        case .closing, .installing, .complete, .legacyComplete:
            return false
        case .recovery, .reopened:
            return true
        }
    }
}

struct UpdateProgressState {
    let request: UpdateRequest
    private var waitingSince: Double

    init(request: UpdateRequest, now: Double) {
        self.request = request
        waitingSince = now
    }

    mutating func keepWaiting(now: Double) { waitingSince = now }

    func stage(now: Double, parentAlive: Bool, completion: UpdateCompletion?, failure: String?, legacy: LegacyLaunchEvidence? = nil) -> UpdateStage {
        // A stale or unrelated acknowledgement never dismisses the only visible UI.
        if !parentAlive, let completion, completion.matches(request) { return .complete }
        if !parentAlive, let legacy, legacy.launched(request, now: now) {
            return legacy.visibleWindow ? .legacyComplete : .reopened
        }
        if let failure, !failure.isEmpty { return .recovery(failure) }
        // A normal update just closes AO and reopens it, and even a slow-but-
        // healthy machine can take a while at each step. Stay hidden (accessory,
        // no window) for the whole budget so nothing pops in front of a working
        // update; the app's own loader is what the user should see on reopen.
        // Only a genuinely stuck update, no close or no reopen after three
        // minutes, is worth surfacing a window with recovery options for.
        if now - waitingSince < 180_000 {
            return parentAlive ? .closing : .installing
        }
        return .recovery(parentAlive
            ? "AO is taking longer than expected to close. The update will continue when AO has closed."
            : "AO has not reopened yet. The installer may still be working. You can keep waiting or download the latest app.")
    }
}
