import SwiftUI
import WrinkleCore

/// A user's icon as its emoji in a circle; without one, the name's first letter.
struct Avatar: View {
    let name: String
    let icon: String
    var size: CGFloat = 40

    var body: some View {
        ZStack {
            Circle().fill(Color.accentColor.opacity(0.15))
            if let emoji = UserIcon.emoji(for: icon) {
                Text(emoji).font(.system(size: size * 0.55))
            } else {
                Text(initial)
                    .font(.system(size: size * 0.45, weight: .semibold))
                    .foregroundStyle(.tint)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }

    private var initial: String { name.first.map { String($0).uppercased() } ?? "" }
}
