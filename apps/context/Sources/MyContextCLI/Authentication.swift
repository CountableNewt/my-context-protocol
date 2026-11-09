import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
#if canImport(Darwin)
import Darwin
#else
import Glibc
#endif

/// Bind only loopback and accept a single nonce-checked OAuth handoff.
final class CallbackListener: @unchecked Sendable {
    let descriptor: Int32
    let port: UInt16
    init() throws {
        #if os(Linux)
        let fd = socket(AF_INET, Int32(SOCK_STREAM.rawValue), 0)
        #else
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        #endif
        guard fd >= 0 else { throw CLIError.request("Unable to create login listener") }
        var address = sockaddr_in()
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = 0
        address.sin_addr.s_addr = inet_addr("127.0.0.1")
        let result = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        guard result == 0 && listen(fd, 8) == 0 else { close(fd); throw CLIError.request("Unable to bind login listener") }
        var length = socklen_t(MemoryLayout<sockaddr_in>.size)
        guard withUnsafeMutablePointer(to: &address, { pointer in pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(fd, $0, &length) } }) == 0 else { close(fd); throw CLIError.request("Unable to inspect login listener") }
        _ = fcntl(fd, F_SETFL, O_NONBLOCK)
        descriptor = fd; port = UInt16(bigEndian: address.sin_port)
    }
    deinit { close(descriptor) }
    func token(nonce: String, seconds: Double = 300) async throws -> String {
        let deadline = Date().addingTimeInterval(seconds)
        while Date() < deadline {
            try Task.checkCancellation()
            let connection = accept(descriptor, nil, nil)
            if connection < 0 { try await Task.sleep(for: .milliseconds(25)); continue }
            _ = fcntl(connection, F_SETFL, O_NONBLOCK)
            #if os(macOS)
            var noSignal: Int32 = 1
            _ = setsockopt(connection, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
            #endif
            defer { close(connection) }
            let readDeadline = Date().addingTimeInterval(5)
            var request = Data(), buffer = [UInt8](repeating: 0, count: 2048)
            while Date() < readDeadline && request.count < 16384 {
                try Task.checkCancellation()
                let count = recv(connection, &buffer, buffer.count, 0)
                if count > 0 { request.append(contentsOf: buffer.prefix(count)); if request.range(of: Data("\r\n\r\n".utf8)) != nil { break } }
                else if count == 0 { break }
                else { try await Task.sleep(for: .milliseconds(25)) }
            }
            let line = String(decoding: request, as: UTF8.self).components(separatedBy: "\r\n").first ?? ""
            let parts = line.split(separator: " ")
            var status = "400 Bad Request", message = "Invalid login callback", token: String?, denied = false
            if parts.count == 3 && parts[0] == "GET", let target = URLComponents(string: String(parts[1])), target.path == "/callback" {
                let query = target.queryItems ?? []
                let states = query.filter { $0.name == "state" }
                if states.count == 1 && constantTimeEqual(states[0].value ?? "", nonce) {
                    let errors = query.filter { $0.name == "error" }
                    let tokens = query.filter { $0.name == "auth_token" }
                    if errors.count == 1 && !(errors[0].value ?? "").isEmpty { denied = true; message = "GitHub authorization failed. Return to MyContextCLI to retry." }
                    else if tokens.count == 1, let value = tokens[0].value, !value.isEmpty {
                        token = value; status = "200 OK"; message = "Authorization received. You can return to MyContextCLI."
                    }
                }
            }
            let response = Data("HTTP/1.1 \(status)\r\nContent-Type: text/plain; charset=utf-8\r\nCache-Control: no-store\r\nReferrer-Policy: no-referrer\r\nConnection: close\r\nContent-Length: \(message.utf8.count)\r\n\r\n\(message)".utf8)
            response.withUnsafeBytes { pointer in
                #if os(Linux)
                _ = send(connection, pointer.baseAddress, pointer.count, Int32(MSG_NOSIGNAL))
                #else
                _ = send(connection, pointer.baseAddress, pointer.count, 0)
                #endif
            }
            if denied { throw CLIError.request("GitHub authorization was declined or failed; run auth login to try again") }
            if let token { return token }
        }
        throw CLIError.request("Login timed out after 5 minutes")
    }
}
func constantTimeEqual(_ left: String, _ right: String) -> Bool {
    let a = Array(left.utf8), b = Array(right.utf8)
    guard a.count == b.count else { return false }
    var difference: UInt8 = 0
    for index in a.indices { difference |= a[index] ^ b[index] }
    return difference == 0
}
func authorizationURL(client: Client, callback: String) async throws -> String {
    var anonymous = client; anonymous.session = ""
    let (_, response) = try await anonymous.request("GET", "/auth/cli/github", query: [URLQueryItem(name: "return_to", value: callback)])
    if response.statusCode == 404 { throw CLIError.request("Server does not support CLI login; deploy the accompanying Gateway version or verify the API origin") }
    guard [302, 303].contains(response.statusCode), let location = response.value(forHTTPHeaderField: "Location"), let url = URLComponents(string: location),
          url.scheme == "https", url.host == "github.com", url.port == nil, url.user == nil, url.password == nil, url.percentEncodedPath == "/login/oauth/authorize", url.fragment == nil else { throw CLIError.request("CLI login returned an invalid GitHub authorization redirect") }
    let query = url.queryItems ?? []
    for key in ["client_id", "state"] {
        let entries = query.filter { $0.name == key }
        guard entries.count == 1, !(entries[0].value ?? "").isEmpty else { throw CLIError.request("CLI login returned an invalid GitHub authorization redirect") }
    }
    return location
}
func browserLogin(client: inout Client, noBrowser: Bool, diagnostics: (String) -> Void) async throws -> String {
    let listener = try CallbackListener()
    var generator = SystemRandomNumberGenerator()
    let nonce = Data((0..<32).map { _ in UInt8.random(in: .min ... .max, using: &generator) }).base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
    let callback = "http://127.0.0.1:\(listener.port)/callback?state=\(nonce)"
    let url = try await authorizationURL(client: client, callback: callback)
    diagnostics("Open this URL to authenticate with GitHub:\n\(url)\n")
    if !noBrowser {
        let process = Process()
        #if os(macOS)
        process.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        #else
        process.executableURL = URL(fileURLWithPath: "/usr/bin/xdg-open")
        #endif
        process.arguments = [url]
        process.standardOutput = FileHandle.nullDevice; process.standardError = FileHandle.nullDevice
        do { try process.run() } catch { diagnostics("Browser could not open automatically; open the URL above.\n") }
    }
    let token = try await listener.token(nonce: nonce)
    let (_, response) = try await client.checked("GET", "/auth/confirm", query: [URLQueryItem(name: "token", value: token)])
    let cookies = HTTPCookie.cookies(withResponseHeaderFields: response.allHeaderFields.reduce(into: [String: String]()) { result, pair in result[String(describing: pair.key)] = String(describing: pair.value) }, for: URL(string: client.origin)!)
    guard let session = cookies.last(where: { $0.name == "vapor-session" && !$0.value.isEmpty })?.value else { throw CLIError.request("Authorization did not return a session cookie") }
    if client.webOrigin.isEmpty, let origin = response.value(forHTTPHeaderField: "X-Context-Frontend-Origin"), let validated = try? validateOrigin(origin) { client.webOrigin = validated }
    client.session = session
    _ = try await client.checked("GET", "/auth/me")
    return session
}
