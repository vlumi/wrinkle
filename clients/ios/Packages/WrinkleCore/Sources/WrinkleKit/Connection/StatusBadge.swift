import SwiftUI
import WrinkleCore

struct StatusBadge: View {
    let state: ConnectionState

    var body: some View {
        let (text, color): (String, Color) =
            switch state {
            case .connected: ("online", .green)
            case .connecting: ("connecting", .orange)
            case .offline: ("offline", .red)
            }
        Label {
            Text(text).font(.caption).fixedSize()
        } icon: {
            Circle().fill(color).frame(width: 8, height: 8)
        }
        .labelStyle(.titleAndIcon)
    }
}
