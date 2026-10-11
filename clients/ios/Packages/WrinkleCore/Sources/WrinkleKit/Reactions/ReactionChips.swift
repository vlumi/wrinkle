import SwiftUI
import WrinkleCore

/// The reactions under a message: each emoji with its count, the user's own highlighted. A tap
/// toggles the user's own; a long press shows who reacted.
struct ReactionChips: View {
    let chips: [ReactionRules.Chip]
    let names: ([IDHex]) -> [String]
    let toggle: (String) -> Void

    var body: some View {
        FlowLayout(spacing: 4) {
            ForEach(chips, id: \.emoji) { chip in
                Button {
                    toggle(chip.emoji)
                } label: {
                    Text("\(chip.emoji) \(chip.count)")
                        .font(.caption)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 3)
                        .background(
                            chip.mine ? Color.accentColor.opacity(0.25) : Color.gray.opacity(0.15),
                            in: .capsule
                        )
                        .overlay {
                            if chip.mine { Capsule().stroke(Color.accentColor, lineWidth: 1) }
                        }
                }
                .buttonStyle(.plain)
                .contextMenu {
                    Section("\(chip.emoji) reacted by") {
                        ForEach(Array(names(chip.users).enumerated()), id: \.offset) { _, name in
                            Text(name)
                        }
                    }
                }
                .accessibilityLabel("\(chip.emoji), \(chip.count)")
                .accessibilityHint(chip.mine ? "Removes your reaction" : "Adds your reaction")
            }
        }
    }
}

/// Lays out its views in rows, wrapping when a row is full.
struct FlowLayout: Layout {
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(subviews, width: proposal.width ?? .infinity)
        return CGSize(
            width: rows.map(\.width).max() ?? 0,
            height: rows.map(\.height).reduce(0, +) + spacing * CGFloat(max(0, rows.count - 1)))
    }

    func placeSubviews(
        in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()
    ) {
        var y = bounds.minY
        for row in arrange(subviews, width: bounds.width) {
            var x = bounds.minX
            for i in row.items {
                let size = subviews[i].sizeThatFits(.unspecified)
                subviews[i].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row {
        var items: [Int] = []
        var width: CGFloat = 0
        var height: CGFloat = 0
    }

    private func arrange(_ subviews: Subviews, width: CGFloat) -> [Row] {
        var rows: [Row] = []
        var row = Row()
        for (i, view) in subviews.enumerated() {
            let size = view.sizeThatFits(.unspecified)
            if !row.items.isEmpty, row.width + spacing + size.width > width {
                rows.append(row)
                row = Row()
            }
            row.width += (row.items.isEmpty ? 0 : spacing) + size.width
            row.height = max(row.height, size.height)
            row.items.append(i)
        }
        if !row.items.isEmpty { rows.append(row) }
        return rows
    }
}
