import Foundation

#if canImport(FoundationNetworking)
  import FoundationNetworking
#endif

/// URLSession serializes delegate callbacks; only cancellation crosses that queue.
final class BoundedResponse: NSObject, URLSessionDataDelegate, @unchecked Sendable {
  var completion: (@Sendable (Result<(Data, HTTPURLResponse), any Error>) -> Void)?
  private var data = Data()
  private var failure: (any Error)?
  func urlSession(
    _ session: URLSession, task: URLSessionTask,
    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
    completionHandler: @escaping @Sendable (URLRequest?) -> Void
  ) { completionHandler(nil) }
  func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive chunk: Data) {
    guard data.count + chunk.count <= 32 << 20 else {
      failure = CLIError.request("Response exceeds 32 MiB")
      dataTask.cancel()
      return
    }
    data.append(chunk)
  }
  func urlSession(
    _ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?
  ) {
    if let error = failure ?? error {
      completion?(.failure(error))
    } else if let response = task.response as? HTTPURLResponse {
      completion?(.success((data, response)))
    } else {
      completion?(.failure(CLIError.request("Invalid HTTP response")))
    }
    completion = nil
  }
}
/// Synchronizes cancellation before and after task installation without races.
final class RequestCancellation: @unchecked Sendable {
  private let lock = NSLock()
  private var task: URLSessionDataTask?
  private var canceled = false
  func start(_ task: URLSessionDataTask) {
    lock.lock()
    self.task = task
    if canceled { task.cancel() }
    task.resume()
    lock.unlock()
  }
  func cancel() {
    lock.lock()
    canceled = true
    task?.cancel()
    lock.unlock()
  }
}
struct Client {
  var origin: String
  var session: String = ""
  var webOrigin: String = ""
  var timeout: Double = 30
  func request(_ method: String, _ path: String, query: [URLQueryItem] = [], body: Data? = nil)
    async throws -> (Data, HTTPURLResponse)
  {
    guard !path.unicodeScalars.contains(where: { $0.value < 32 || $0.value == 127 }),
      path.hasPrefix("/"), !path.hasPrefix("//"), !path.contains("\\"),
      var relative = URLComponents(string: path), relative.scheme == nil, relative.host == nil,
      relative.fragment == nil
    else { throw CLIError.usage("API path must be an origin-relative path") }
    relative.queryItems = (relative.queryItems ?? []) + query
    if relative.queryItems?.isEmpty == true { relative.queryItems = nil }
    guard let url = URL(string: origin + (relative.string ?? path)),
      url.host == URL(string: origin)?.host
    else { throw CLIError.usage("Invalid API path") }
    var request = URLRequest(url: url)
    request.httpMethod = method
    request.httpBody = body
    request.timeoutInterval = timeout
    request.setValue("application/json", forHTTPHeaderField: "Accept")
    request.setValue("MyContextCLI", forHTTPHeaderField: "User-Agent")
    if body != nil { request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
    if !webOrigin.isEmpty && method != "GET" {
      request.setValue(webOrigin, forHTTPHeaderField: "Origin")
    }
    if !session.isEmpty {
      guard !session.contains(where: { $0.isNewline || $0 == ";" || $0 == "\0" }) else {
        throw CLIError.usage("Invalid session cookie")
      }
      request.setValue("vapor-session=\(session)", forHTTPHeaderField: "Cookie")
    }
    let configuration = URLSessionConfiguration.ephemeral
    configuration.httpShouldSetCookies = false
    configuration.httpCookieStorage = nil
    configuration.timeoutIntervalForRequest = timeout
    configuration.timeoutIntervalForResource = timeout
    let delegate = BoundedResponse()
    let transport = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    let cancellation = RequestCancellation()
    defer { transport.invalidateAndCancel() }
    return try await withTaskCancellationHandler {
      try await withCheckedThrowingContinuation { continuation in
        delegate.completion = { continuation.resume(with: $0) }
        cancellation.start(transport.dataTask(with: request))
      }
    } onCancel: {
      cancellation.cancel()
    }
  }
  func checked(_ method: String, _ path: String, query: [URLQueryItem] = [], body: Data? = nil)
    async throws -> (Data, HTTPURLResponse)
  {
    let (data, response) = try await request(method, path, query: query, body: body)
    guard (200..<300).contains(response.statusCode) else {
      let payload: JSON
      if (300..<400).contains(response.statusCode) {
        payload = .object([
          "location": .string(response.value(forHTTPHeaderField: "Location") ?? "")
        ])
      } else {
        payload =
          (try? JSON(data: data))
          ?? .object([
            "message": .string(HTTPURLResponse.localizedString(forStatusCode: response.statusCode))
          ])
      }
      throw CLIError.api(response.statusCode, payload)
    }
    return (data, response)
  }
}
