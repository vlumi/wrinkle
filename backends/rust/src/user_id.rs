//! User ids are 16 bytes; Valkey keys and channels name a user by the
//! lowercase hex of the id.

use std::fmt::Write as _;

pub type UserId = [u8; 16];

pub fn hex(id: &UserId) -> String {
    id.iter().fold(String::with_capacity(32), |mut s, b| {
        let _ = write!(s, "{b:02x}");
        s
    })
}

/// The id named by its lowercase hex, as `hex` writes it.
pub fn parse_hex(s: &str) -> Option<UserId> {
    if s.len() != 32 {
        return None;
    }
    let mut id = UserId::default();
    for (i, pair) in s.as_bytes().chunks(2).enumerate() {
        let digit = |c: u8| match c {
            b'0'..=b'9' => Some(c - b'0'),
            b'a'..=b'f' => Some(c - b'a' + 10),
            _ => None,
        };
        id[i] = digit(pair[0])? << 4 | digit(pair[1])?;
    }
    Some(id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hex_is_lowercase_and_round_trips() {
        let mut id = UserId::default();
        id[0] = 0xff;
        id[15] = 0x01;
        assert_eq!(hex(&id), "ff000000000000000000000000000001");
        assert_eq!(parse_hex(&hex(&id)), Some(id));
    }

    #[test]
    fn parse_hex_refuses_other_forms() {
        assert_eq!(parse_hex("FF000000000000000000000000000001"), None);
        assert_eq!(parse_hex("ff00"), None);
        assert_eq!(parse_hex("123"), None);
        assert_eq!(parse_hex("zz000000000000000000000000000001"), None);
    }
}
