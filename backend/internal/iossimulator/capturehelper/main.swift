import Foundation
import ScreenCaptureKit
import CoreMedia
import CoreVideo
import ImageIO
import UniformTypeIdentifiers
import VideoToolbox

// Native capture helper for the AO iOS Simulator panel.
//
// Two modes, selected by argv:
//   - --once   (default) capture a single PNG frame of the Simulator window and
//              write it to stdout, then exit. Used by the REST screenshot path
//              and by GraphQL-free ad-hoc captures.
//   - --stream keep the ScreenCaptureKit stream alive and write every frame to
//              stdout as a 4-byte big-endian length followed by the PNG bytes.
//              The Go daemon supervises this process (restart on exit) and
//              multiplexes its frames to WebSocket subscribers, so the panel
//              never spawns a capture process per frame.
//
// The window selection is deliberate: AO owns one simulator, so the first
// Simulator-owned window is the managed device. `excludingDesktopWindows(false,
// onScreenWindowsOnly: false)` allows capturing windows that are hidden or on
// another Space, so Simulator.app does not need to be visible on screen.
@main
struct SimulatorCaptureProbe {
    static func main() async {
        let streaming = CommandLine.arguments.dropFirst().contains("--stream")
        let h264 = CommandLine.arguments.dropFirst().contains("--h264")
        do {
            // ScreenCaptureKit initializes CoreGraphics through AppKit. A
            // command-line process must create an accessory NSApplication
            // before requesting shareable content or CoreGraphics aborts with
            // CGS_REQUIRE_INIT.
            let application = NSApplication.shared
            application.setActivationPolicy(.accessory)
            application.finishLaunching()
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
            guard let window = content.windows.first(where: { $0.owningApplication?.applicationName == "Simulator" }) else {
                throw NSError(domain: "AO.Capture", code: 1, userInfo: [NSLocalizedDescriptionKey: "Simulator window not found"])
            }
            let filter = SCContentFilter(desktopIndependentWindow: window)
            let configuration = SCStreamConfiguration()
            configuration.width = max(Int(window.frame.width * 2), 1)
            configuration.height = max(Int(window.frame.height * 2), 1)
            configuration.pixelFormat = kCVPixelFormatType_32BGRA
            configuration.queueDepth = 3
            let stream = SCStream(filter: filter, configuration: configuration, delegate: StreamErrorDelegate())
            let output = FrameOutput(streaming: streaming)
            let encoder = h264 ? H264Output(width: configuration.width, height: configuration.height) : nil
            if let encoder { try stream.addStreamOutput(encoder, type: .screen, sampleHandlerQueue: DispatchQueue(label: "ao.encode")) }
            if h264 {
                try await stream.startCapture()
                _ = try await withCheckedThrowingContinuation { (_: CheckedContinuation<Void, Error>) in }
                return
            }
            try stream.addStreamOutput(output, type: .screen, sampleHandlerQueue: DispatchQueue(label: "ao.capture"))
            try await stream.startCapture()
            if streaming {
                // Never return: frames are written from the sample queue until
                // the daemon terminates this process.
                _ = try await withCheckedThrowingContinuation { (_: CheckedContinuation<Void, Error>) in
                    // Intentionally unresolved.
                }
            } else {
                let image = try await output.nextImage()
                try await stream.stopCapture()
                FileHandle.standardOutput.write(try encodePNG(image))
            }
        } catch { FileHandle.standardError.write(Data("AO capture: \(error)\n".utf8)); exit(1) }
    }

    static func encodePNG(_ image: CGImage) throws -> Data {
        let data = NSMutableData()
        guard let destination = CGImageDestinationCreateWithData(data, UTType.png.identifier as CFString, 1, nil) else {
            throw NSError(domain: "AO.Capture", code: 2, userInfo: [NSLocalizedDescriptionKey: "cannot create PNG destination"])
        }
        CGImageDestinationAddImage(destination, image, nil)
        guard CGImageDestinationFinalize(destination) else {
            throw NSError(domain: "AO.Capture", code: 3, userInfo: [NSLocalizedDescriptionKey: "cannot finalize PNG"])
        }
        return data as Data
    }
}

/// Terminates the helper when ScreenCaptureKit stops the stream (for example
/// when the Simulator window disappears), so the Go supervisor can restart it
/// once the device is available again.
final class StreamErrorDelegate: NSObject, SCStreamDelegate {
    func stream(_ stream: SCStream, didStopWithError error: Error) {
        FileHandle.standardError.write(Data("AO capture: stream stopped: \(error)\n".utf8))
        exit(1)
    }
}

final class FrameOutput: NSObject, SCStreamOutput {
    private let streaming: Bool
    private let context = CIContext()
    private let lock = NSLock()
    private var continuation: CheckedContinuation<CGImage, Error>?

    init(streaming: Bool) {
        self.streaming = streaming
        super.init()
    }

    func nextImage() async throws -> CGImage {
        try await withCheckedThrowingContinuation { continuation = $0 }
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen, let pixelBuffer = sampleBuffer.imageBuffer else { return }
        let ci = CIImage(cvPixelBuffer: pixelBuffer)
        guard let image = context.createCGImage(ci, from: ci.extent) else { return }
        if streaming {
            guard let png = try? SimulatorCaptureProbe.encodePNG(image) else { return }
            writeFrame(png)
            return
        }
        lock.lock()
        if let waiter = continuation {
            continuation = nil
            waiter.resume(returning: image)
        }
        lock.unlock()
    }

    /// Writes one length-prefixed PNG frame. Big-endian length keeps the Go
    /// reader's framing trivial and stable across endianness.
    private func writeFrame(_ png: Data) {
        var length = UInt32(png.count).bigEndian
        var out = Data()
        out.reserveCapacity(4 + png.count)
        withUnsafeBytes(of: &length) { out.append(contentsOf: $0) }
        out.append(png)
        FileHandle.standardOutput.write(out)
    }
}

/// Encodes the IOSurface-backed CVPixelBuffer delivered by ScreenCaptureKit
/// directly with VideoToolbox. The output is one Annex-B access unit per
/// length-prefixed record: [u32 payload][u32 width][u32 height][bytes].
final class H264Output: NSObject, SCStreamOutput {
    private let width: Int
    private let height: Int
    private var session: VTCompressionSession?
    private let lock = NSLock()

    init(width: Int, height: Int) {
        self.width = width
        self.height = height
        super.init()
        VTCompressionSessionCreate(allocator: nil, width: Int32(width), height: Int32(height), codecType: kCMVideoCodecType_H264, encoderSpecification: nil, imageBufferAttributes: nil, compressedDataAllocator: nil, outputCallback: h264Callback, refcon: Unmanaged.passUnretained(self).toOpaque(), compressionSessionOut: &session)
        if let session {
            VTSessionSetProperty(session, key: kVTCompressionPropertyKey_RealTime, value: kCFBooleanTrue)
            VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AllowFrameReordering, value: kCFBooleanFalse)
            VTCompressionSessionPrepareToEncodeFrames(session)
        }
    }

    deinit { if let session { VTCompressionSessionInvalidate(session) } }

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen, let buffer = CMSampleBufferGetImageBuffer(sampleBuffer), let session else { return }
        let pts = CMSampleBufferGetPresentationTimeStamp(sampleBuffer)
        VTCompressionSessionEncodeFrame(session, imageBuffer: buffer, presentationTimeStamp: pts, duration: .invalid, frameProperties: nil, sourceFrameRefcon: nil, infoFlagsOut: nil)
    }

    fileprivate func write(_ sample: CMSampleBuffer) {
        guard let block = CMSampleBufferGetDataBuffer(sample) else { return }
        var length = 0
        var pointer: UnsafeMutablePointer<Int8>?
        guard CMBlockBufferGetDataPointer(block, atOffset: 0, lengthAtOffsetOut: nil, totalLengthOut: &length, dataPointerOut: &pointer) == noErr, let pointer else { return }
        var bytes = Data()
        var offset = 0
        while offset + 4 <= length {
            let nRaw = pointer.advanced(by: offset).withMemoryRebound(to: UInt32.self, capacity: 1) { $0.pointee }
            let n = Int(UInt32(bigEndian: nRaw))
            if n <= 0 || offset + 4 + n > length { return }
            bytes.append(contentsOf: [0, 0, 0, 1])
            bytes.append(Data(bytes: pointer.advanced(by: offset + 4), count: n))
            offset += 4 + n
        }
        guard !bytes.isEmpty else { return }
        var header = Data()
        var payloadLength = UInt32(bytes.count).bigEndian
        var frameWidth = UInt32(width).bigEndian
        var frameHeight = UInt32(height).bigEndian
        withUnsafeBytes(of: &payloadLength) { header.append(contentsOf: $0) }
        withUnsafeBytes(of: &frameWidth) { header.append(contentsOf: $0) }
        withUnsafeBytes(of: &frameHeight) { header.append(contentsOf: $0) }
        lock.lock(); defer { lock.unlock() }
        FileHandle.standardOutput.write(header)
        FileHandle.standardOutput.write(bytes)
    }
}

private func h264Callback(_ outputCallbackRefCon: UnsafeMutableRawPointer?, _ sourceFrameRefCon: UnsafeMutableRawPointer?, _ status: OSStatus, _ infoFlags: VTEncodeInfoFlags, _ sampleBuffer: CMSampleBuffer?) {
    guard status == noErr, let sampleBuffer, let outputCallbackRefCon else { return }
    Unmanaged<H264Output>.fromOpaque(outputCallbackRefCon).takeUnretainedValue().write(sampleBuffer)
}
