import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

final class NoRedirect: NSObject, URLSessionTaskDelegate, Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) { completionHandler(nil) }
}
struct Client {
    var origin: String
    var session: String = ""
    var webOrigin: String = ""
    var timeout: Double = 30
    func request(_ method: String, _ path: String, query: [URLQueryItem] = [], body: Data? = nil) async throws -> (Data, HTTPURLResponse) {
        guard path.hasPrefix("/"), !path.hasPrefix("//"), !path.contains("\\"),
              var relative = URLComponents(string: path), relative.scheme == nil, relative.host == nil, relative.fragment == nil else { throw CLIError.usage("API path must be an origin-relative path") }
        relative.queryItems = (relative.queryItems ?? []) + query
        if relative.queryItems?.isEmpty == true { relative.queryItems = nil }
        guard let url = URL(string: origin + (relative.string ?? path)), url.host == URL(string: origin)?.host else { throw CLIError.usage("Invalid API path") }
        var request = URLRequest(url: url)
        request.httpMethod = method; request.httpBody = body; request.timeoutInterval = timeout
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("MyContextCLI", forHTTPHeaderField: "User-Agent")
        if body != nil { request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        if !webOrigin.isEmpty && method != "GET" { request.setValue(webOrigin, forHTTPHeaderField: "Origin") }
        if !session.isEmpty {
            guard !session.contains(where: { $0.isNewline || $0 == ";" || $0 == "\0" }) else { throw CLIError.usage("Invalid session cookie") }
            request.setValue("vapor-session=\(session)", forHTTPHeaderField: "Cookie")
        }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpShouldSetCookies = false; configuration.httpCookieStorage = nil
        configuration.timeoutIntervalForRequest = timeout; configuration.timeoutIntervalForResource = timeout
        let transport = URLSession(configuration: configuration, delegate: NoRedirect(), delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        // Stream the body so the response limit also bounds memory consumption.
        #if os(macOS)
        let (bytes, response) = try await transport.bytes(for: request)
        var data = Data()
        for try await byte in bytes {
            guard data.count < 32 << 20 else { throw CLIError.request("Response exceeds 32 MiB") }
            data.append(byte)
        }
        #else
        let (data, response) = try await transport.data(for: request)
        guard data.count <= 32 << 20 else { throw CLIError.request("Response exceeds 32 MiB") }
        #endif
        guard let response = response as? HTTPURLResponse else { throw CLIError.request("Invalid HTTP response") }
        return (data, response)
    }
    func checked(_ method: String, _ path: String, query: [URLQueryItem] = [], body: Data? = nil) async throws -> (Data, HTTPURLResponse) {
        let (data, response) = try await request(method, path, query: query, body: body)
        guard (200..<300).contains(response.statusCode) else {
            let payload: JSON
            if (300..<400).contains(response.statusCode) { payload = .object(["location": .string(response.value(forHTTPHeaderField: "Location") ?? "")]) }
            else { payload = (try? JSON(data: data)) ?? .object(["message": .string(HTTPURLResponse.localizedString(forStatusCode: response.statusCode))]) }
            throw CLIError.api(response.statusCode, payload)
        }
        return (data, response)
    }
}
