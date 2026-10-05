import Foundation
import AppKit
import ScreenCaptureKit
import CoreMedia
import Darwin
import os

@available(macOS 15.0, *)
final class Recording: NSObject, SCRecordingOutputDelegate {
    var stream: SCStream?
    var stopSignal: DispatchSourceSignal?
    let readyURL: URL
    private let stopRequested = OSAllocatedUnfairLock(initialState: false)

    init(readyURL: URL) { self.readyURL = readyURL }

    func recordingOutputDidStartRecording(_ output: SCRecordingOutput) {
        do { try Data().write(to: readyURL) }
        catch { fail(error) }
    }

    func recordingOutput(_ output: SCRecordingOutput, didFailWithError error: Error) {
        fail(error)
    }

    func recordingOutputDidFinishRecording(_ output: SCRecordingOutput) {
        guard stopRequested.withLock({ $0 }) else {
            fputs("error: window recording ended before the demo completed\n", stderr)
            exit(1)
        }
        exit(0)
    }

    func requestStop() {
        stopRequested.withLock { $0 = true }
        Task { @MainActor in
            guard let stream else {
                fputs("error: window recording stopped before capture started\n", stderr)
                exit(1)
            }
            do { try await stream.stopCapture() }
            catch { fail(error) }
        }
    }

    func fail(_ error: Error) {
        fputs("error: window recording: \(error.localizedDescription)\n", stderr)
        exit(1)
    }
}

if #available(macOS 15.0, *) {
    let application = NSApplication.shared
    application.setActivationPolicy(.prohibited)
    guard CommandLine.arguments.count == 4,
          let windowID = UInt32(CommandLine.arguments[1]) else {
        fputs("usage: record-window <Terminal window ID> <output.mp4> <ready file>\n", stderr)
        exit(1)
    }
    let recording = Recording(readyURL: URL(fileURLWithPath: CommandLine.arguments[3]))
    signal(SIGINT, SIG_IGN)
    recording.stopSignal = DispatchSource.makeSignalSource(signal: SIGINT, queue: .main)
    recording.stopSignal?.setEventHandler {
        recording.requestStop()
    }
    recording.stopSignal?.resume()
    Task { @MainActor in
        do {
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
            guard let window = content.windows.first(where: {
                $0.windowID == windowID && $0.owningApplication?.bundleIdentifier == "com.apple.Terminal"
            }) else {
                throw NSError(domain: "record-window", code: 1, userInfo: [NSLocalizedDescriptionKey: "owned Terminal window is unavailable"])
            }
            let filter = SCContentFilter(desktopIndependentWindow: window)
            let configuration = SCStreamConfiguration()
            configuration.width = min(2560, Int(filter.contentRect.width * CGFloat(filter.pointPixelScale))) / 2 * 2
            configuration.height = Int(CGFloat(configuration.width) * filter.contentRect.height / filter.contentRect.width) / 2 * 2
            configuration.minimumFrameInterval = CMTime(value: 1, timescale: 15)
            configuration.showsCursor = false
            configuration.capturesAudio = false
            let output = SCRecordingOutputConfiguration()
            output.outputURL = URL(fileURLWithPath: CommandLine.arguments[2])
            let stream = SCStream(filter: filter, configuration: configuration, delegate: nil)
            recording.stream = stream
            try stream.addRecordingOutput(SCRecordingOutput(configuration: output, delegate: recording))
            try await stream.startCapture()
        } catch { recording.fail(error) }
    }
    application.run()
} else {
    fputs("error: independent window recording requires macOS 15 or later\n", stderr)
    exit(1)
}
