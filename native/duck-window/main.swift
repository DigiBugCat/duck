import AppKit
import WebKit
import Foundation

final class StdoutWriter {
    private let queue = DispatchQueue(label: "duck-window.stdout")

    func write(_ object: [String: Any]) {
        queue.async {
            guard JSONSerialization.isValidJSONObject(object),
                  let data = try? JSONSerialization.data(withJSONObject: object, options: []),
                  var line = String(data: data, encoding: .utf8) else {
                return
            }
            line.append("\n")
            if let out = line.data(using: .utf8) {
                FileHandle.standardOutput.write(out)
            }
        }
    }
}

final class DuckMarkHandler: NSObject, WKScriptMessageHandler {
    private let out: StdoutWriter

    init(out: StdoutWriter) {
        self.out = out
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        if let text = message.body as? String,
           let data = text.data(using: .utf8),
           let mark = try? JSONSerialization.jsonObject(with: data, options: []) {
            out.write(["mark": mark])
            return
        }
        out.write(["mark": message.body])
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private let out = StdoutWriter()
    private var window: NSWindow!
    private var webView: WKWebView!

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)

        let runtimePath = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ""
        let runtime = (try? String(contentsOfFile: runtimePath, encoding: .utf8)) ?? ""

        let controller = WKUserContentController()
        controller.addUserScript(WKUserScript(source: runtime, injectionTime: .atDocumentStart, forMainFrameOnly: false))
        controller.add(DuckMarkHandler(out: out), name: "duckMark")

        let config = WKWebViewConfiguration()
        config.userContentController = controller

        webView = WKWebView(frame: .zero, configuration: config)
        window = NSWindow(
            contentRect: NSRect(x: 200, y: 200, width: 1200, height: 800),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Duck Window"
        window.contentView = webView
        window.makeKeyAndOrderFront(nil)

        startInputReader()
        out.write(["ready": true])
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    private func startInputReader() {
        DispatchQueue.global(qos: .userInitiated).async {
            while let line = readLine() {
                self.handle(line: line)
            }
            DispatchQueue.main.async {
                NSApp.terminate(nil)
            }
        }
    }

    private func handle(line: String) {
        guard let data = line.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data, options: []),
              let command = object as? [String: Any] else {
            return
        }
        if let urlString = command["navigate"] as? String {
            DispatchQueue.main.async {
                if let url = URL(string: urlString) {
                    self.webView.load(URLRequest(url: url))
                    self.window.makeKeyAndOrderFront(nil)
                    NSApp.activate(ignoringOtherApps: true)
                }
            }
            return
        }
        if let js = command["eval"] as? String {
            DispatchQueue.main.async {
                self.webView.evaluateJavaScript(js, completionHandler: nil)
            }
        }
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
