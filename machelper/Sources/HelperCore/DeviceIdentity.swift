import CryptoKit
import Foundation

/// How a paired device is told apart from another one: a short fingerprint of
/// its public key, and a "skeleton" of its name that folds look-alike letters.
public enum DeviceIdentity {
    /// The first 8 bytes of SHA-256 over the 32-byte public key, as four groups of
    /// four lowercase hex digits ("1a2b 3c4d 5e6f 7a8b"). The Linux side computes
    /// the same string (`fingerprint` in cmd/sharelinux), so a person can compare
    /// the two. 64 bits are too many to grind for a look-alike by accident, and
    /// short enough to read aloud.
    public static func fingerprint(_ key: Data) -> String {
        let digest = Array(SHA256.hash(data: key).prefix(8))
        let hex = digest.map { String(format: "%02x", $0) }
        return stride(from: 0, to: hex.count, by: 2).map { hex[$0] + hex[$0 + 1] }.joined(separator: " ")
    }

    /// Letters that look like another letter, mapped to that letter. A reasonable
    /// subset of Unicode TR39 "confusables" (Cyrillic and Greek that pass for
    /// Latin, plus the Latin ones: I, l, 1 and | for each other, 0 for o). The
    /// full table is not shipped with the SDK; this errs towards refusing.
    private static let confusables: [Character: Character] = [
        // Cyrillic (lowercase after folding)
        "а": "a", "е": "e", "о": "o", "р": "p", "с": "c", "у": "y", "х": "x", "і": "l", "ј": "j", "ѕ": "s",
        "ԁ": "d", "һ": "h", "ԛ": "q", "ԝ": "w", "ѵ": "v", "ӏ": "l", "ь": "b", "к": "k", "м": "m", "т": "t",
        "в": "b", "н": "h", "ё": "e",
        // Greek (lowercase after folding)
        "α": "a", "β": "b", "ε": "e", "ι": "l", "κ": "k", "ν": "v", "ο": "o", "ρ": "p", "τ": "t", "υ": "u",
        "χ": "x", "γ": "y", "η": "n", "ω": "w", "μ": "u",
        // Latin and digits
        "1": "l", "|": "l", "ǀ": "l", "ı": "l", "0": "o", "5": "s", "ɡ": "g", "ɑ": "a", "ᴏ": "o", "ᴄ": "c",
        "ƅ": "b", "ɩ": "l", "ꞵ": "b", "ό": "o", "ø": "o", "đ": "d", "ł": "l", "ŧ": "t", "ħ": "h",
        "ɪ": "l", "ʟ": "l", "ᴅ": "d", "ᴍ": "m", "ɴ": "n", "ʀ": "r", "ᴛ": "t", "ᴜ": "u", "ᴠ": "v", "ᴡ": "w",
        "ʏ": "y", "ᴢ": "z", "ᴀ": "a", "ʙ": "b", "ɢ": "g", "ʜ": "h", "ᴊ": "j", "ᴋ": "k", "ᴘ": "p",
    ]

    /// Two names with the same skeleton look alike (or nearly): compatibility
    /// forms (fullwidth, ligatures) become plain, accents go, capital I becomes
    /// l before case folding, look-alike letters from other scripts become the
    /// Latin letter, "rn" becomes m, "vv" becomes w, "cl" becomes d, and spaces
    /// and the usual separators are dropped. Pure and deterministic.
    public static func skeleton(_ name: String) -> String {
        var s = ""
        for ch in name.precomposedStringWithCompatibilityMapping.decomposedStringWithCanonicalMapping.unicodeScalars {
            if ch.properties.generalCategory == .nonspacingMark || ch.properties.generalCategory == .enclosingMark
                || ch.properties.generalCategory == .spacingMark
            { continue }
            s.unicodeScalars.append(ch)
        }
        s = s.replacingOccurrences(of: "I", with: "l")  // capital i is an l in most sans-serif faces
        s = s.lowercased()
        var folded = ""
        for ch in s {
            if " -_.\u{2010}\u{2011}\u{00B7}".contains(ch) { continue }
            folded.append(confusables[ch] ?? ch)
        }
        // Two letters that pass for one.
        folded = folded.replacingOccurrences(of: "rn", with: "m")
            .replacingOccurrences(of: "vv", with: "w")
            .replacingOccurrences(of: "cl", with: "d")
        return folded
    }

    /// True when `name` would be confused with `existing` (equal skeletons).
    public static func looksAlike(_ name: String, _ existing: String) -> Bool {
        skeleton(name) == skeleton(existing)
    }
}
