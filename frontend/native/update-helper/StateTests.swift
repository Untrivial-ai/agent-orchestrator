import Foundation

@main
struct StateTests {
    static func main() throws {
        let request = UpdateRequest(version: "1.2.3", appPath: "/Applications/AO.app", parentPID: 123, startedAt: 1000)
        var state = UpdateProgressState(request: request, now: 1000)
        func completion(_ json: String) throws -> UpdateCompletion {
            try JSONDecoder().decode(UpdateCompletion.self, from: Data(json.utf8))
        }
        let valid = try completion("{\"version\":\"1.2.3\",\"appPath\":\"/Applications/AO.app\",\"parentPID\":123,\"startedAt\":1000,\"pid\":456}")
        assert(state.stage(now: 1001, parentAlive: true, completion: nil, failure: nil) == .closing)
        assert(state.stage(now: 1001, parentAlive: false, completion: nil, failure: nil) == .installing)
        if case .recovery = state.stage(now: 31_000, parentAlive: true, completion: nil, failure: nil) {} else { fatalError("closing deadline") }
        if case .recovery = state.stage(now: 181_000, parentAlive: false, completion: nil, failure: nil) {} else { fatalError("restart deadline") }
        state.keepWaiting(now: 190_000)
        assert(state.stage(now: 190_001, parentAlive: false, completion: nil, failure: nil) == .installing)
        assert(state.stage(now: 190_001, parentAlive: true, completion: valid, failure: nil) == .closing)
        assert(state.stage(now: 190_001, parentAlive: false, completion: valid, failure: "late error") == .complete)
        assert(state.stage(now: 190_001, parentAlive: false, completion: nil, failure: "Failed") == .recovery("Failed"))
        for field in ["version", "appPath", "parentPID", "startedAt", "pid"] {
            var json: [String: Any] = ["version": "1.2.3", "appPath": "/Applications/AO.app", "parentPID": 123, "startedAt": 1000, "pid": 456]
            json[field] = (field == "version" || field == "appPath") ? "wrong" : (field == "pid" ? 123 : 999)
            let mismatch = try JSONDecoder().decode(UpdateCompletion.self, from: JSONSerialization.data(withJSONObject: json))
            assert(!mismatch.matches(request), "mismatch \(field)")
        }
        let zeroPID = try completion("{\"version\":\"1.2.3\",\"appPath\":\"/Applications/AO.app\",\"parentPID\":123,\"startedAt\":1000,\"pid\":0}")
        assert(!zeroPID.matches(request))
        let legacyMarker = LegacyAppMarker(appPath: request.appPath, version: request.version,
                                           lastReconciledAt: "1970-01-01T00:00:01.000Z", updateRestartProtocol: nil)
        func evidence(marker: LegacyAppMarker? = legacyMarker, pid: Int32? = 456,
                      appPath: String? = request.appPath, finished: Bool = true, visible: Bool = true) -> LegacyLaunchEvidence {
            LegacyLaunchEvidence(marker: marker, pid: pid, appPath: appPath, finishedLaunching: finished, visibleWindow: visible)
        }
        assert(state.stage(now: 190_001, parentAlive: false, completion: nil, failure: nil, legacy: evidence()) == .legacyComplete)
        assert(state.stage(now: 190_001, parentAlive: false, completion: nil, failure: nil, legacy: evidence(visible: false)) == .reopened)
        assert(state.stage(now: 190_001, parentAlive: true, completion: nil, failure: nil, legacy: evidence()) == .closing)
        for invalid in [evidence(marker: nil), evidence(pid: nil), evidence(pid: request.parentPID), evidence(appPath: "/wrong/AO.app"), evidence(finished: false)] {
            assert(state.stage(now: 190_001, parentAlive: false, completion: nil, failure: nil, legacy: invalid) == .installing)
        }
        let invalidMarkers = [
            LegacyAppMarker(appPath: request.appPath, version: request.version, lastReconciledAt: "1970-01-01T00:00:00.999Z", updateRestartProtocol: nil),
            LegacyAppMarker(appPath: "/wrong/AO.app", version: request.version, lastReconciledAt: legacyMarker.lastReconciledAt, updateRestartProtocol: nil),
            LegacyAppMarker(appPath: request.appPath, version: "9.9.9", lastReconciledAt: legacyMarker.lastReconciledAt, updateRestartProtocol: nil),
            LegacyAppMarker(appPath: request.appPath, version: request.version, lastReconciledAt: legacyMarker.lastReconciledAt, updateRestartProtocol: 1),
            LegacyAppMarker(appPath: request.appPath, version: request.version, lastReconciledAt: legacyMarker.lastReconciledAt, updateRestartProtocol: 2),
            LegacyAppMarker(appPath: request.appPath, version: request.version, lastReconciledAt: "garbage", updateRestartProtocol: nil),
            LegacyAppMarker(appPath: request.appPath, version: request.version, lastReconciledAt: "2999-01-01T00:00:01.000Z", updateRestartProtocol: nil),
        ]
        for marker in invalidMarkers {
            assert(!evidence(marker: marker).launched(request, now: 190_001))
            assert(state.stage(now: 190_001, parentAlive: false, completion: nil, failure: nil, legacy: evidence(marker: marker)) == .installing)
        }
        print("Update helper state tests passed")
    }
}
