import XCTest

@testable import WrinkleCore

final class AccountsTests: XCTestCase {
    func testHexRoundTrip() {
        let id = Data([0xff] + Array(repeating: 0, count: 14) + [0x01])
        XCTAssertEqual(id.hex, "ff000000000000000000000000000001")
        XCTAssertEqual(Data(hex: id.hex), id)
        XCTAssertEqual(Data(hex: "FF00"), Data([0xff, 0]))
        XCTAssertNil(Data(hex: "abc"))
        XCTAssertNil(Data(hex: "zz"))
        XCTAssertEqual(Data.id(hex: id.hex), id)
        XCTAssertNil(Data.id(hex: "ff00"))
    }

    func testNameRules() {
        XCTAssertNil(AccountRules.nameProblem("bob"))
        XCTAssertNil(AccountRules.nameProblem("Anna Maria"))
        XCTAssertNil(AccountRules.nameProblem(String(repeating: "x", count: 64)))
        XCTAssertNotNil(AccountRules.nameProblem(""))
        XCTAssertNotNil(AccountRules.nameProblem(String(repeating: "x", count: 65)))
        // 22 four-byte characters are 88 bytes.
        XCTAssertNotNil(AccountRules.nameProblem(String(repeating: "🦊", count: 22)))
        XCTAssertNotNil(AccountRules.nameProblem(" bob"))
        XCTAssertNotNil(AccountRules.nameProblem("bob\u{00a0}"))
        XCTAssertNotNil(AccountRules.nameProblem("bo\u{7}b"))
    }

    func testPasswordRules() {
        XCTAssertNil(AccountRules.passwordProblem("12345678"))
        XCTAssertNotNil(AccountRules.passwordProblem("1234567"))
        XCTAssertNotNil(AccountRules.passwordProblem(String(repeating: "x", count: 257)))
    }

    func testDeviceNameIsCutWithoutSplittingCharacters() {
        let name = AccountRules.deviceName(String(repeating: "ä", count: 40))
        XCTAssertEqual(name.utf8.count, 64)
        XCTAssertEqual(AccountRules.deviceName(String(repeating: "🦊", count: 20)).utf8.count, 64)
        XCTAssertEqual(
            AccountRules.deviceName("x" + String(repeating: "🦊", count: 20)).utf8.count, 61)
    }

    func testIcons() {
        XCTAssertEqual(UserIcon.all.count, 12)
        XCTAssertEqual(UserIcon.emoji(for: "owl"), "🦉")
        XCTAssertNil(UserIcon.emoji(for: ""))
        XCTAssertNil(UserIcon.emoji(for: "dragon"))
    }
}
