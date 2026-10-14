import Foundation
import WrinkleCore

/// Words an error for the user. The backend's messages are for logs, so its codes are mapped
/// here, with the meanings that depend on the request given by the caller.
func errorText(_ error: Error, _ meanings: [Wrinkle_V1_ErrorCode: String] = [:]) -> String {
    if let e = error as? AccountError { return e.description }
    guard let e = error as? BackendError else {
        return "Connection lost; try again."
    }
    if let meaning = meanings[e.code] { return meaning }
    switch e.code {
    case .invalidArgument: return "The server did not accept that."
    case .notFound: return "Not found."
    case .unavailable, .resourceExhausted: return "The server is busy; try again."
    default: return "Something went wrong (\(e.code))."
    }
}

/// A failure the client itself detected, worded for the user.
public struct AccountError: Error, CustomStringConvertible {
    public let description: String
    init(_ description: String) { self.description = description }
}
