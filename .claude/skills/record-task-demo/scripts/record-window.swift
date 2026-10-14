import Foundation
import AppKit
import ScreenCaptureKit
import CoreMedia
import CoreGraphics
import Darwin
import os

struct Viewport {
    let x: Double
    let y: Double
    let width: Double
    let height: Double
    let rows: Int
    let columns: Int
    let windowBounds: CGRect
    let windowID: CGWindowID
}

func fail(_ message: String) -> Never {
    fputs("error: window recorder: \(message)\n", stderr)
    exit(1)
}

func close(_ lhs: CGFloat, _ rhs: CGFloat, tolerance: CGFloat = 1) -> Bool {
    abs(lhs - rhs) <= tolerance
}

func sameRect(_ lhs: CGRect, _ rhs: CGRect, tolerance: CGFloat = 1) -> Bool {
    close(lhs.minX, rhs.minX, tolerance: tolerance) &&
    close(lhs.minY, rhs.minY, tolerance: tolerance) &&
    close(lhs.width, rhs.width, tolerance: tolerance) &&
    close(lhs.height, rhs.height, tolerance: tolerance)
}

func parseViewport(_ arguments: [String]) -> Viewport {
    guard arguments.count == 14,
          let x = Double(arguments[3]),
          let y = Double(arguments[4]),
          let width = Double(arguments[5]),
          let height = Double(arguments[6]),
          let rows = Int(arguments[7]),
          let columns = Int(arguments[8]),
          let left = Double(arguments[9]),
          let top = Double(arguments[10]),
          let right = Double(arguments[11]),
          let bottom = Double(arguments[12]),
          let windowID = UInt32(arguments[13]), windowID > 0,
          x.isFinite, y.isFinite, width.isFinite, height.isFinite,
          x >= 0, y > 0, width > 0, height > 0,
          rows > 0, columns > 0, right > left, bottom > top else {
        fail("usage: record-window <output.mp4> <ready> <TTY x y width height rows columns> <window left top right bottom> <owned window id>")
    }
    return Viewport(
        x: x, y: y, width: width, height: height, rows: rows, columns: columns,
        windowBounds: CGRect(x: left, y: top, width: right - left, height: bottom - top),
        windowID: windowID
    )
}

func cgRect(_ info: [String: Any]) -> CGRect? {
    guard let dictionary = info[kCGWindowBounds as String] as? [String: Any] else { return nil }
    return CGRect(dictionaryRepresentation: dictionary as CFDictionary)
}

func cgWindows(for processID: pid_t) -> [(id: CGWindowID, frame: CGRect)] {
    guard let entries = CGWindowListCopyWindowInfo(
        [.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID
    ) as? [[String: Any]] else {
        fail("CoreGraphics could not enumerate on-screen window frames")
    }
    return entries.compactMap { entry in
        guard let owner = entry[kCGWindowOwnerPID as String] as? Int32,
              owner == processID,
              let number = entry[kCGWindowNumber as String] as? UInt32,
              let frame = cgRect(entry) else { return nil }
        return (number, frame)
    }
}

func measuredContentRect(
    viewport: Viewport,
    processID: pid_t,
    window: SCWindow,
    filter: SCContentFilter
) -> (viewport: CGRect, surface: CGRect) {
    let onscreen = cgWindows(for: processID)
    let bases = onscreen.filter { sameRect($0.frame, viewport.windowBounds) }
    guard bases.count == 1, bases[0].id == window.windowID else {
        fail("TTY-matched Terminal bounds do not identify one on-screen capture window")
    }
    guard sameRect(window.frame, bases[0].frame) else {
        fail("ScreenCaptureKit and CoreGraphics disagree about the owned Terminal window frame")
    }
    let frame = bases[0].frame
    let headers = onscreen.filter { candidate in
        candidate.id != bases[0].id &&
        close(candidate.frame.minX, frame.minX) &&
        close(candidate.frame.width, frame.width) &&
        candidate.frame.minY < frame.minY &&
        candidate.frame.maxY > frame.minY
    }
    guard headers.count == 1 else {
        fail("Terminal native tab-header geometry cannot be uniquely measured against the owned window")
    }

    // Calibrate Terminal's reported viewport units against the measured native
    // header-to-window boundary; no fixed titlebar/header inset is assumed.
    let pointsPerTerminalUnit = (frame.minY - headers[0].frame.minY) / viewport.y
    guard pointsPerTerminalUnit.isFinite, pointsPerTerminalUnit > 0 else {
        fail("Terminal viewport units cannot be calibrated against measured window geometry")
    }
    let origin = CGPoint(
        x: headers[0].frame.minX + viewport.x * pointsPerTerminalUnit,
        y: headers[0].frame.minY + viewport.y * pointsPerTerminalUnit
    )
    guard close(origin.x, frame.minX) && close(origin.y, frame.minY) else {
        fail("Terminal viewport origin does not meet the measured owned-window content boundary")
    }
    let source = CGRect(
        x: origin.x,
        y: origin.y,
        width: viewport.width * pointsPerTerminalUnit,
        height: viewport.height * pointsPerTerminalUnit
    )
    let cellWidth = source.width / CGFloat(viewport.columns)
    let cellHeight = source.height / CGFloat(viewport.rows)
    let rightPadding = frame.maxX - source.maxX
    let bottomPadding = frame.maxY - source.maxY
    guard cellWidth.isFinite, cellHeight.isFinite,
          cellWidth > 0, cellHeight > 0,
          rightPadding >= 0, bottomPadding >= 0,
          rightPadding <= cellWidth, bottomPadding <= cellHeight,
          frame.contains(source), filter.contentRect.contains(source) else {
        fail("measured Terminal text viewport is not wholly contained in the owned ScreenCaptureKit window")
    }
    return (viewport: source, surface: headers[0].frame.union(frame))
}

@available(macOS 15.0, *)
final class Recording: NSObject, SCRecordingOutputDelegate {
    var stream: SCStream?
    var stopSignal: DispatchSourceSignal?
    let readyURL: URL
    private let stopRequested = OSAllocatedUnfairLock(initialState: false)

    init(readyURL: URL) { self.readyURL = readyURL }

    func recordingOutputDidStartRecording(_ output: SCRecordingOutput) {
        do { try Data().write(to: readyURL) }
        catch { fail("cannot create readiness marker: \(error.localizedDescription)") }
    }

    func recordingOutput(_ output: SCRecordingOutput, didFailWithError error: Error) {
        fail(error.localizedDescription)
    }

    func recordingOutputDidFinishRecording(_ output: SCRecordingOutput) {
        guard stopRequested.withLock({ $0 }) else {
            fail("window recording ended before the demo completed")
        }
        exit(0)
    }

    func requestStop() {
        stopRequested.withLock { $0 = true }
        Task { @MainActor in
            guard let stream else { fail("recording stopped before capture started") }
            do { try await stream.stopCapture() }
            catch { fail(error.localizedDescription) }
        }
    }

    func fail(_ message: String) -> Never {
        fputs("error: window recorder: \(message)\n", stderr)
        exit(1)
    }
}

@available(macOS 15.0, *)
func runRecorder(_ arguments: [String], viewport: Viewport) {
    let terminals = NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.Terminal")
    guard terminals.count == 1, let terminal = terminals.first else {
        fail("Terminal process identity is ambiguous")
    }
    let recording = Recording(readyURL: URL(fileURLWithPath: arguments[2]))
    signal(SIGINT, SIG_IGN)
    recording.stopSignal = DispatchSource.makeSignalSource(signal: SIGINT, queue: .main)
    recording.stopSignal?.setEventHandler { recording.requestStop() }
    recording.stopSignal?.resume()

    Task { @MainActor in
        do {
            let shareable = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
            let candidates = shareable.windows.filter {
                $0.windowID == viewport.windowID && $0.isOnScreen &&
                $0.owningApplication?.bundleIdentifier == "com.apple.Terminal" &&
                $0.owningApplication?.processID == terminal.processIdentifier &&
                sameRect($0.frame, viewport.windowBounds)
            }
            guard candidates.count == 1, let window = candidates.first else {
                throw NSError(domain: "record-window", code: 1, userInfo: [
                    NSLocalizedDescriptionKey: "TTY-matched visible Terminal window is not unique"
                ])
            }
            let filter = SCContentFilter(desktopIndependentWindow: window)
            let measurement = measuredContentRect(
                viewport: viewport,
                processID: terminal.processIdentifier,
                window: window,
                filter: filter
            )
            // Window capture includes the native Terminal tab strip. Translate
            // the measured text viewport from display coordinates into that
            // measured header-plus-window surface, not the base SCWindow frame.
            let surface = measurement.surface
            let sourceRect = CGRect(
                x: measurement.viewport.minX - surface.minX,
                y: measurement.viewport.minY - surface.minY,
                width: measurement.viewport.width,
                height: measurement.viewport.height
            )
            guard CGRect(origin: .zero, size: surface.size).contains(sourceRect) else {
                throw NSError(domain: "record-window", code: 4, userInfo: [
                    NSLocalizedDescriptionKey: "measured text viewport does not fit the native Terminal capture surface"
                ])
            }
            let scale = CGFloat(filter.pointPixelScale)
            guard scale.isFinite, scale > 0 else {
                throw NSError(domain: "record-window", code: 2, userInfo: [
                    NSLocalizedDescriptionKey: "ScreenCaptureKit returned invalid point/pixel scale"
                ])
            }
            let sourceWidth = Int(sourceRect.width * scale)
            let outputWidth = min(2560, sourceWidth) / 2 * 2
            let outputHeight = Int(Double(outputWidth) * Double(sourceRect.height / sourceRect.width)) / 2 * 2
            guard outputWidth >= 2, outputHeight >= 2 else {
                throw NSError(domain: "record-window", code: 3, userInfo: [
                    NSLocalizedDescriptionKey: "measured selected-tab viewport is too small to record"
                ])
            }
            let configuration = SCStreamConfiguration()
            configuration.sourceRect = sourceRect
            configuration.width = outputWidth
            configuration.height = outputHeight
            configuration.minimumFrameInterval = CMTime(value: 1, timescale: 15)
            configuration.showsCursor = false
            configuration.capturesAudio = false
            let output = SCRecordingOutputConfiguration()
            output.outputURL = URL(fileURLWithPath: arguments[1])
            let stream = SCStream(filter: filter, configuration: configuration, delegate: nil)
            recording.stream = stream
            try stream.addRecordingOutput(SCRecordingOutput(configuration: output, delegate: recording))
            try await stream.startCapture()
        } catch {
            recording.fail(error.localizedDescription)
        }
    }
    NSApplication.shared.setActivationPolicy(.prohibited)
    NSApplication.shared.run()
}

let arguments = CommandLine.arguments
let viewport = parseViewport(arguments)
if #available(macOS 15.0, *) {
    runRecorder(arguments, viewport: viewport)
} else {
    fail("selected-tab ScreenCaptureKit recording requires macOS 15 or later")
}
