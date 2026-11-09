import Foundation

public struct CLI {
  public init() {}
  public func main(
    args: [String], env: [String: String] = ProcessInfo.processInfo.environment,
    input: FileHandle = .standardInput,
    output: (String) -> Void = { FileHandle.standardOutput.write(Data($0.utf8)) },
    diagnostics: (String) -> Void = { FileHandle.standardError.write(Data($0.utf8)) },
    version: String = "dev"
  ) async -> Int32 {
    do {
      try await run(
        args: args, env: env, input: input, output: output, diagnostics: diagnostics,
        version: version)
      return 0
    } catch {
      let failure: CLIError
      if error is CancellationError || (error as? URLError)?.code == .cancelled {
        failure = .canceled
      } else {
        failure = (error as? CLIError) ?? .request("\(error.localizedDescription)")
      }
      if Options.jsonErrors(args) {
        var values: [String: JSON] = [
          "error": .string(failure.message),
          "code": .string(
            [2: "invalid_arguments", 3: "unauthenticated", 4: "forbidden", 130: "canceled"][
              failure.exitCode] ?? "request_failed"),
        ]
        if case .api(let status, let details) = failure {
          values["status"] = .number(String(status))
          values["details"] = details
        }
        diagnostics(JSON.object(values).rendered() + "\n")
      } else {
        var message = "Error: \(readable(failure.message))\n"
        var hint = "Check your connection and the selected API origin, then try again."
        if case .api(let status, let details) = failure {
          message += "HTTP \(status)\n"
          if case .object(let fields) = details {
            for key in ["error", "reason", "message"] {
              if case .string(let detail) = fields[key], !detail.isEmpty {
                message += readable(detail) + "\n"
                break
              }
            }
          }
          switch status {
          case 401:
            hint = "Run myctx auth login for the selected profile and API origin, then try again."
          case 403:
            hint =
              "Check your account permissions. For session imports, set --web-url to the dashboard origin."
          case 404: hint = "Check the resource ID and selected API origin."
          case 429: hint = "Wait before retrying; the API rate limit was reached."
          case 400..<500: hint = "Check the command arguments and request data with myctx help."
          default: break
          }
        }
        if failure.exitCode == 2 { hint = "Run myctx help or myctx help <group> for usage." }
        if failure.exitCode == 130 { hint = "Run the command again when ready." }
        diagnostics(message + "Hint: \(hint)\n")
      }
      return failure.exitCode
    }
  }

  private func run(
    args: [String], env: [String: String], input: FileHandle, output: (String) -> Void,
    diagnostics: (String) -> Void, version: String
  ) async throws {
    let options = try Options(args)
    var name = options.words.joined(separator: " ")
    func emit(_ value: JSON) { output(value.rendered(pretty: options["compact"].isEmpty) + "\n") }
    if !options["version"].isEmpty || name == "version" {
      emit(.object(["name": .string("MyContextCLI"), "version": .string(version)]))
      return
    }
    if !options["help"].isEmpty || name.isEmpty || options.words.first == "help" {
      let prefix = options.words.filter { $0 != "help" }.joined(separator: " ")
      output(
        "MyContextCLI — agent-friendly MyContextProtocol CLI\n\nUsage: myctx <group> <command> [flags]\nHosted service: api.mycontextprotocol.dev (override with --url for self-hosting)\n\nAuthentication: auth login [--no-browser] | auth login --session-stdin | auth status | auth logout\nConfiguration: config show | config set --url ORIGIN [--project ID]\nDiscovery: commands (JSON) | api operations (JSON) | help\nRaw API: api METHOD /path [--data JSON|@file|-] [--query key=value]\n\nGlobal flags: --url ORIGIN --web-url ORIGIN --profile NAME --config FILE --project ID --timeout 30s --compact --json\nRequest flags: --id ID --release-id ID --compiled-skill-id ID --key-id ID\n               --data JSON|@file|- --set field=JSON --query key=value (repeatable)\nEnvironment: MYCTX_URL, MYCTX_WEB_URL, MYCTX_SESSION, MYCTX_PROFILE, MYCTX_CONFIG, MYCTX_PROJECT (legacy CONTEXT_* also supported)\n\nCommands:\n"
      )
      var lastGroup = ""
      for operation in operations
      where prefix.isEmpty || operation.name == prefix || operation.name.hasPrefix(prefix + " ") {
        let group = String(operation.name.split(separator: " ")[0])
        if !lastGroup.isEmpty && lastGroup != group { output("\n") }
        lastGroup = group
        output(
          "  "
            + operation.name.padding(
              toLength: max(28, operation.name.count), withPad: " ", startingAt: 0) + " "
            + operation.description + "\n")
      }
      return
    }
    if name == "commands" || name == "api operations" {
      emit(
        .array(
          operations.map {
            .object([
              "name": .string($0.name), "method": .string($0.method), "path": .string($0.path),
              "description": .string($0.description),
            ])
          }))
      return
    }
    let timeout = try duration(first(options["timeout"], "30s"))
    let path = first(options["config"], Configuration.path(env))
    var config = try Configuration.load(path)
    let profileName = first(options["profile"], environment("PROFILE", env), "default")
    var profile = config.profiles[profileName] ?? Profile()
    let origin = try validateOrigin(
      first(
        options["url"], environment("URL", env), profile.url, "https://api.mycontextprotocol.dev"))
    if name == "config show" {
      emit(
        .object([
          "profile": .string(profileName), "url": .string(origin),
          "web_url": .string(profile.webURL), "project": .string(profile.project),
          "authenticated": .bool(!profile.session.isEmpty || !environment("SESSION", env).isEmpty),
          "config": .string(path),
        ]))
      return
    }
    if name == "config set" {
      if !profile.url.isEmpty && profile.url != origin { profile = Profile() }
      profile.url = origin
      if !options["web-url"].isEmpty { profile.webURL = try validateOrigin(options["web-url"]) }
      if !options["project"].isEmpty { profile.project = options["project"] }
      config.profiles[profileName] = profile
      try config.save(path)
      emit(.object(["ok": .bool(true)]))
      return
    }
    let session = first(environment("SESSION", env), profile.url == origin ? profile.session : "")
    let web = first(
      options["web-url"], environment("WEB_URL", env), profile.url == origin ? profile.webURL : "")
    var client = Client(
      origin: origin, session: session, webOrigin: web.isEmpty ? "" : try validateOrigin(web),
      timeout: timeout)
    if name == "auth login" {
      let credential: String
      if !options["session-stdin"].isEmpty {
        let raw = try boundedRead(input, limit: 8192)
        credential = String(decoding: raw, as: UTF8.self).trimmingCharacters(
          in: .whitespacesAndNewlines)
        guard
          !credential.isEmpty
            && !credential.contains(where: { $0.isNewline || $0 == ";" || $0 == "\0" })
        else { throw CLIError.usage("Stdin must contain a single session cookie value") }
        client.session = credential
        _ = try await client.checked("GET", "/auth/me")
      } else {
        credential = try await browserLogin(
          client: &client, noBrowser: !options["no-browser"].isEmpty, diagnostics: diagnostics)
      }
      if profile.url != origin { profile.project = "" }
      profile.url = origin
      profile.webURL = client.webOrigin
      profile.session = credential
      config.profiles[profileName] = profile
      try config.save(path)
      emit(.object(["ok": .bool(true), "profile": .string(profileName)]))
      return
    }
    if name == "auth logout" {
      if !session.isEmpty {
        do { _ = try await client.checked("POST", "/auth/logout") } catch CLIError.api(401, _) {
          // Expired sessions still clear local credentials.
        }
      }
      if profile.url == origin {
        profile.session = ""
        config.profiles[profileName] = profile
        try config.save(path)
      }
      emit(.object(["ok": .bool(true)]))
      return
    }
    if name == "auth status" { name = "auth me" }
    var method: String
    var route: String
    if options.words.first == "api" {
      guard options.words.count == 3 else { throw CLIError.usage("Usage: myctx api METHOD /path") }
      method = options.words[1].uppercased()
      route = options.words[2]
      guard ["GET", "POST", "PATCH", "PUT", "DELETE"].contains(method) else {
        throw CLIError.usage("Unsupported HTTP method")
      }
    } else {
      guard let operation = operations.first(where: { $0.name == name }) else {
        throw CLIError.usage("Unknown command \(name); run myctx help")
      }
      method = operation.method
      route = operation.path
    }
    let project = first(
      options["id"], options["project"], environment("PROJECT", env),
      profile.url == origin ? profile.project : "")
    let replacements = [
      "id": project, "releaseId": options["release-id"],
      "compiledSkillId": options["compiled-skill-id"], "keyId": options["key-id"],
    ]
    for segment in route.split(separator: "/") where segment.hasPrefix(":") {
      let key = String(segment.dropFirst())
      let value = replacements[String(segment.dropFirst())] ?? ""
      guard !value.isEmpty else {
        throw CLIError.usage("Missing path parameter \(key); see myctx help")
      }
      guard value != ".", value != "..",
        value.rangeOfCharacter(from: CharacterSet(charactersIn: "/\\?#%")) == nil
      else { throw CLIError.usage("Invalid path parameter \(key)") }
      let encoded = value.addingPercentEncoding(
        withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-._~")))!
      route = route.replacingOccurrences(of: String(segment), with: encoded)
    }
    let query = try (options.flags["query"] ?? []).map { value -> URLQueryItem in
      let pieces = value.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
      guard pieces.count == 2, !pieces[0].isEmpty else {
        throw CLIError.usage("--query requires key=value")
      }
      return URLQueryItem(name: String(pieces[0]), value: String(pieces[1]))
    }
    let body: Data?
    do { body = try requestBody(options, input) } catch let error as CLIError { throw error } catch
    { throw CLIError.usage("Invalid request body: \(error.localizedDescription)") }
    if ["GET", "DELETE"].contains(method) && body != nil {
      throw CLIError.usage("\(method) does not accept --data or --set")
    }
    if name == "github install" {
      let (data, response) = try await client.request(method, route, query: query, body: body)
      if (300..<400).contains(response.statusCode) {
        emit(.object(["url": .string(response.value(forHTTPHeaderField: "Location") ?? "")]))
        return
      }
      guard (200..<300).contains(response.statusCode) else {
        throw CLIError.api(response.statusCode, (try? JSON(data: data)) ?? .null)
      }
      emit(responseJSON(data))
      return
    }
    let (data, _) = try await client.checked(method, route, query: query, body: body)
    emit(responseJSON(data))
  }
}
func readable(_ text: String) -> String {
  String(
    text.unicodeScalars.map { $0.value < 32 || $0.value == 127 ? " " : String($0) }.joined().prefix(
      2000))
}
func responseJSON(_ data: Data) -> JSON {
  if data.isEmpty { return .object(["ok": .bool(true)]) }
  return (try? JSON(data: data))
    ?? .object(["result": .string(String(decoding: data, as: UTF8.self))])
}
func boundedRead(_ input: FileHandle, limit: Int) throws -> Data {
  var result = Data()
  while let chunk = try input.read(upToCount: min(65536, limit + 1 - result.count)), !chunk.isEmpty
  {
    result.append(chunk)
    guard result.count <= limit else { throw CLIError.usage("Input exceeds \(limit) bytes") }
  }
  return result
}
func requestBody(_ options: Options, _ input: FileHandle) throws -> Data? {
  var json: JSON?
  if !options["data"].isEmpty {
    let source = options["data"]
    let data: Data
    if source == "-" {
      data = try boundedRead(input, limit: 8 << 20)
    } else if source.hasPrefix("@") {
      let handle = try FileHandle(forReadingFrom: URL(fileURLWithPath: String(source.dropFirst())))
      defer { try? handle.close() }
      data = try boundedRead(handle, limit: 8 << 20)
    } else {
      data = Data(source.utf8)
      guard data.count <= 8 << 20 else { throw CLIError.usage("Request exceeds 8 MiB") }
    }
    json = try JSON(data: data)
  }
  if let assignments = options.flags["set"] {
    var object: [String: JSON] = [:]
    if let json {
      guard case .object(let value) = json else {
        throw CLIError.usage("--set requires an object body")
      }
      object = value
    }
    for assignment in assignments {
      let pieces = assignment.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
      guard pieces.count == 2, !pieces[0].isEmpty else {
        throw CLIError.usage("--set requires field=JSON")
      }
      object[String(pieces[0])] = try JSON(data: Data(pieces[1].utf8))
    }
    json = .object(object)
  }
  guard let json else { return nil }
  let data = Data(json.rendered().utf8)
  guard data.count <= 8 << 20 else { throw CLIError.usage("Request exceeds 8 MiB") }
  return data
}
func duration(_ raw: String) throws -> Double {
  let pattern = #"([0-9]+(?:\.[0-9]+)?)(ms|us|µs|ns|s|m|h)"#
  let expression = try NSRegularExpression(pattern: pattern)
  let range = NSRange(raw.startIndex..., in: raw)
  let matches = expression.matches(in: raw, range: range)
  guard !matches.isEmpty,
    matches.map({ (raw as NSString).substring(with: $0.range) }).joined() == raw
  else { throw CLIError.usage("--timeout must be a positive duration") }
  var total = 0.0
  for match in matches {
    let value = Double((raw as NSString).substring(with: match.range(at: 1)))!
    let unit = (raw as NSString).substring(with: match.range(at: 2))
    total +=
      value * ["ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1, "m": 60, "h": 3600][unit]!
  }
  guard total > 0 && total.isFinite else {
    throw CLIError.usage("--timeout must be a positive duration")
  }
  return total
}
