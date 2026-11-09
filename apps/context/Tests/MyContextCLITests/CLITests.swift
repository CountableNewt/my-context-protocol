import Foundation
import Testing

@testable import MyContextCLI

@Test func exactJSONNumbersAndRoundTrip() throws {
  let source = Data(
    #"{"large":123456789012345678901234567890,"scientific":1.23456789e99,"nested":[null,true,"a\nb"]}"#
      .utf8)
  let value = try JSON(data: source)
  #expect(value.rendered().contains("123456789012345678901234567890"))
  #expect(value.rendered().contains("1.23456789e99"))
  #expect(try JSON(data: Data(value.rendered(pretty: true).utf8)) == value)
}
@Test func invalidJSONAndDepthAreRejected() {
  for text in [
    "", "[1,]", "{\"a\":}", "01", "true false", "\"\\x\"",
    String(repeating: "[", count: 130) + "0" + String(repeating: "]", count: 130),
  ] {
    #expect(throws: (any Error).self) { try JSON(data: Data(text.utf8)) }
  }
}
@Test func argumentOrderingAndDuplicateValidation() throws {
  let options = try Options([
    "--compact", "projects", "--id=p", "get", "--query", "q=1", "--query=q=2",
  ])
  #expect(options.words == ["projects", "get"])
  #expect(options["id"] == "p")
  #expect(options.flags["query"] == ["q=1", "q=2"])
  for args in [["--unknown"], ["--url"], ["--url=a", "--url=b"], ["--json=true"]] {
    #expect(throws: (any Error).self) { try Options(args) }
  }
  #expect(Options.jsonErrors(["--oops", "--json"]))
  #expect(!Options.jsonErrors(["--data", "--json"]))
}
@Test func configurationCompatibilityAndPermissions() throws {
  let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
  defer { try? FileManager.default.removeItem(at: root) }
  let path = root.appendingPathComponent("myctx/config.json").path
  var configuration = try Configuration.load(path)
  var profile = Profile()
  profile.url = "https://example.com"
  profile.session = "secret"
  configuration.profiles["default"] = profile
  try configuration.save(path)
  #expect(try Configuration.load(path).profiles["default"]?.session == "secret")
  #expect(
    (try FileManager.default.attributesOfItem(atPath: path)[.posixPermissions] as? NSNumber)?
      .intValue == 0o600)
  #expect(
    (try FileManager.default.attributesOfItem(atPath: root.appendingPathComponent("myctx").path)[
      .posixPermissions] as? NSNumber)?.intValue == 0o700)
  for json in ["{}", "{\"profiles\":null}"] {
    #expect(try JSONDecoder().decode(Configuration.self, from: Data(json.utf8)).profiles.isEmpty)
  }
}
@Test func originValidation() throws {
  #expect(try validateOrigin("https://example.com/") == "https://example.com")
  #expect(try validateOrigin("http://127.0.0.1:8080") == "http://127.0.0.1:8080")
  for origin in [
    "http://example.com", "https://user:password@example.com", "https://example.com/path",
    "https://example.com?x=1", "https://example.com#fragment", "https://example.com\r\n",
  ] {
    #expect(throws: (any Error).self) { try validateOrigin(origin) }
  }
}
@Test func requestMergingAndLimits() throws {
  let options = try Options([
    "--data", #"{"n":123456789012345678901234567890,"name":"old"}"#, "--set", #"name="new""#,
    "--set", "nested=[true,null]",
  ])
  let data = try #require(try requestBody(options, .nullDevice))
  #expect(String(decoding: data, as: UTF8.self).contains("123456789012345678901234567890"))
  #expect(
    try JSON(data: data)
      == JSON(
        data: Data(#"{"n":123456789012345678901234567890,"name":"new","nested":[true,null]}"#.utf8))
  )
  #expect(throws: (any Error).self) {
    try requestBody(Options(["--data", "[]", "--set", "x=1"]), .nullDevice)
  }
  #expect(throws: (any Error).self) {
    try requestBody(Options(["--set", "name=unquoted"]), .nullDevice)
  }
}
@Test func durationsAndEnvironment() throws {
  #expect(try duration("1m30s") == 90)
  #expect(try duration("500ms") == 0.5)
  for value in ["0s", "-1s", "30", "nan", "1sx"] {
    #expect(throws: (any Error).self) { try duration(value) }
  }
  #expect(environment("URL", ["MYCTX_URL": "new", "CONTEXT_URL": "old"]) == "new")
  #expect(environment("URL", ["MYCTX_URL": "", "CONTEXT_URL": "old"]) == "old")
}
@Test func operationRegistryCoversGateway() throws {
  let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
  let source = try String(
    contentsOf: root.appendingPathComponent("services/mcp-gateway/Sources/App/routes.swift"),
    encoding: .utf8)
  let regex = try NSRegularExpression(
    pattern: #"(?m)^\s*(?:protected|adminProtected)\.(get|post|patch|put|delete)\(([^\n]+)\)\s*\{"#)
  let segments = try NSRegularExpression(pattern: #""([^"]+)""#)
  var expected: Set<String> = ["GET /health", "GET /auth/me", "POST /auth/logout"]
  for match in regex.matches(in: source, range: NSRange(source.startIndex..., in: source)) {
    let method = (source as NSString).substring(with: match.range(at: 1)).uppercased()
    let args = (source as NSString).substring(with: match.range(at: 2))
    let parts = segments.matches(in: args, range: NSRange(args.startIndex..., in: args)).map {
      (args as NSString).substring(with: $0.range(at: 1))
    }
    expected.insert(method + " /" + parts.joined(separator: "/"))
  }
  #expect(expected.count >= 40)
  let actual = Set(operations.map { $0.method + " " + $0.path })
  #expect(expected == actual)
  #expect(Set(operations.map(\.name)).count == operations.count)
}
