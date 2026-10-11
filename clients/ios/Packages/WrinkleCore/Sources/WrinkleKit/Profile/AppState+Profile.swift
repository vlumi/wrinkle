import CoreGraphics
import Foundation
import WrinkleCore

// MARK: Profile and account

extension AppState {
    /// Changes the given fields of the profile; nil keeps a field.
    public func updateProfile(name: String? = nil, icon: String? = nil) async throws {
        let (connection, _) = try connected()
        me = try await connection.updateProfile(name: name, icon: icon)
    }

    /// Makes a picture, in any format ImageIO reads, the avatar: cropped, scaled and encoded
    /// here, uploaded, then set on the profile.
    public func setAvatar(picture: Data) async throws {
        let (connection, _) = try connected()
        guard let base = blobBase else { throw AccountError("This server has no blob address.") }
        let jpeg = await Task.detached { AvatarImage.jpeg(from: picture) }.value
        guard let jpeg else { throw AccountError("That picture cannot be used.") }
        let slot = try await connection.createUpload(
            purpose: .avatar, mediaType: AvatarImage.mediaType, size: jpeg.count)
        do {
            try await Blobs.shared.upload(
                jpeg, blob: slot.blobID, uploadPath: slot.uploadPath,
                mediaType: AvatarImage.mediaType, base: base)
        } catch {
            throw AccountError("The picture could not be uploaded.")
        }
        me = try await connection.updateProfile(avatar: slot.blobID)
    }

    public func removeAvatar() async throws {
        let (connection, _) = try connected()
        me = try await connection.updateProfile(avatar: Data())
    }

    /// An avatar decoded earlier, if it was.
    func avatarImage(_ blob: String) -> CGImage? { avatarImages[blob] }

    /// Fetches and decodes an avatar, from the disk cache when it is there.
    func loadAvatar(_ blob: String) async {
        guard avatarImages[blob] == nil, let id = Data(hex: blob), !id.isEmpty,
            let base = blobBase,
            let data = try? await Blobs.shared.data(blob: id, base: base),
            let image = AvatarImage.decode(data)
        else { return }
        avatarImages[blob] = image
    }

    /// Changes the password; the account's other devices are signed out.
    public func changePassword(current: String, new: String) async throws {
        let (connection, _) = try connected()
        try await connection.changePassword(current: current, new: new)
    }

    /// Deletes the account after checking the password, and forgets it here.
    public func deleteAccount(password: String) async throws {
        let (connection, _) = try connected()
        do {
            try await connection.deleteAccount(password: password)
        } catch let e as BackendError {
            throw e
        } catch {
            // The close that follows the deletion may beat the response.
            guard await connection.closeCode == CloseCode.signedOut else { throw error }
        }
        endSession(notice: Self.deletedNotice)
    }
}
