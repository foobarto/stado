// hash-id-rust — stado plugin: identify hash types from a sample.
//
// Tools:
//   hash_identify {hash}
//     → [{name, hashcat_mode, john_format, notes}, ...]
//
// This is the Rust SDK proof for stado's polyglot wasm plugin runtime.
// Build target: wasm32-unknown-unknown (no_std, no WASI).
// Expected wasm size: ~50-200 KB (vs ~3.5 MB Go equivalent).
//
// ABI:
//   exports: stado_alloc, stado_free, stado_tool_hash_identify
//   imports: stado_log (optional)
//
// Prerequisites: rustup target add wasm32-unknown-unknown

#![no_std]

// We need an allocator even in no_std for the bump allocator below.
extern crate alloc;
use alloc::vec::Vec;

// ---- Host imports -------------------------------------------------------

extern "C" {
    fn stado_log(
        level_ptr: *const u8, level_len: u32,
        msg_ptr: *const u8, msg_len: u32,
    );
}

#[allow(dead_code)]
fn log_info(msg: &[u8]) {
    unsafe { stado_log(b"info".as_ptr(), 4, msg.as_ptr(), msg.len() as u32); }
}

// ---- Bump allocator -----------------------------------------------------

const ARENA_SIZE: usize = 2 * 1024 * 1024; // 2 MiB (host allocs args + result separately)
static mut ARENA: [u8; ARENA_SIZE] = [0u8; ARENA_SIZE];
static mut BUMP: usize = 0;

#[no_mangle]
pub extern "C" fn stado_alloc(size: u32) -> u32 {
    let n = size as usize;
    unsafe {
        if BUMP + n > ARENA_SIZE { return 0; }
        let ptr = ARENA.as_ptr().add(BUMP) as u32;
        BUMP += n;
        BUMP = (BUMP + 7) & !7; // 8-byte align
        ptr
    }
}

#[no_mangle]
pub extern "C" fn stado_free(_ptr: u32, _size: u32) {}

// Required for no_std wasm — panic handler.
#[cfg(not(test))]
#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    loop {}
}

// ---- Global allocator (required for alloc crate) ------------------------

use core::alloc::{GlobalAlloc, Layout};

struct BumpAlloc;

unsafe impl GlobalAlloc for BumpAlloc {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let size = layout.size();
        let align = layout.align();
        let bump = &raw mut BUMP;
        let arena = ARENA.as_mut_ptr();
        let aligned = ((*bump + align - 1) & !(align - 1));
        if aligned + size > ARENA_SIZE { return core::ptr::null_mut(); }
        *bump = aligned + size;
        arena.add(aligned)
    }
    unsafe fn dealloc(&self, _ptr: *mut u8, _layout: Layout) {}
}

#[global_allocator]
static ALLOCATOR: BumpAlloc = BumpAlloc;

// ---- JSON helpers -------------------------------------------------------

fn json_str<'a>(data: &'a [u8], key: &[u8]) -> Option<&'a [u8]> {
    let mut i = 0;
    while i + key.len() + 2 < data.len() {
        if data[i] != b'"' { i += 1; continue; }
        let ks = i + 1;
        let ke = ks + key.len();
        if ke + 1 >= data.len() { break; }
        if &data[ks..ke] == key && data[ke] == b'"' {
            let mut j = ke + 1;
            while j < data.len() && data[j] != b'"' { j += 1; }
            if j >= data.len() { return None; }
            let start = j + 1;
            let mut end = start;
            while end < data.len() {
                if data[end] == b'\\' { end += 2; continue; }
                if data[end] == b'"' { break; }
                end += 1;
            }
            if end >= data.len() { return None; }
            return Some(&data[start..end]);
        }
        i += 1;
    }
    None
}

fn write_json(result_ptr: *mut u8, result_cap: u32, payload: &[u8]) -> i32 {
    if payload.len() > result_cap as usize { return -1; }
    unsafe {
        core::ptr::copy_nonoverlapping(payload.as_ptr(), result_ptr, payload.len());
    }
    payload.len() as i32
}

// ---- Hash pattern matching ----------------------------------------------

struct HashDef {
    name: &'static str,
    hashcat_mode: &'static str,
    john_format: &'static str,
    notes: &'static str,
    /// Optional: exact hex length this hash must match (0 = any).
    exact_len: usize,
    /// Required prefix/substring for this entry to match.
    prefix: &'static str,
}

const HASH_TABLE: &[HashDef] = &[
    HashDef { name: "Kerberos 5 AS-REP (etype 23)", hashcat_mode: "18200", john_format: "krb5asrep",
        notes: "GetNPUsers.py / Rubeus asreproast", exact_len: 0, prefix: "$krb5asrep$23$" },
    HashDef { name: "Kerberos 5 TGS-REP (etype 23)", hashcat_mode: "13100", john_format: "krb5tgs",
        notes: "GetUserSPNs.py / Rubeus kerberoast", exact_len: 0, prefix: "$krb5tgs$23$" },
    HashDef { name: "NetNTLMv2", hashcat_mode: "5600", john_format: "netntlmv2",
        notes: "Responder/Inveigh capture", exact_len: 0, prefix: "" },  // detected by structure below
    HashDef { name: "bcrypt", hashcat_mode: "3200", john_format: "bcrypt",
        notes: "Slow — targeted wordlist only", exact_len: 0, prefix: "$2" },
    HashDef { name: "sha512crypt", hashcat_mode: "1800", john_format: "sha512crypt",
        notes: "Modern Linux /etc/shadow", exact_len: 0, prefix: "$6$" },
    HashDef { name: "sha256crypt", hashcat_mode: "7400", john_format: "sha256crypt",
        notes: "", exact_len: 0, prefix: "$5$" },
    HashDef { name: "md5crypt", hashcat_mode: "500", john_format: "md5crypt",
        notes: "", exact_len: 0, prefix: "$1$" },
    HashDef { name: "md5crypt (apr)", hashcat_mode: "500", john_format: "md5crypt",
        notes: "", exact_len: 0, prefix: "$apr1$" },
    HashDef { name: "WordPress phpass", hashcat_mode: "400", john_format: "phpass",
        notes: "", exact_len: 0, prefix: "$P$" },
    HashDef { name: "KeePass 1.x", hashcat_mode: "13400", john_format: "keepass",
        notes: "", exact_len: 0, prefix: "$keepass$*1*" },
    HashDef { name: "KeePass 2.x", hashcat_mode: "13400", john_format: "keepass",
        notes: "", exact_len: 0, prefix: "$keepass$*2*" },
    HashDef { name: "Django PBKDF2", hashcat_mode: "20000", john_format: "django",
        notes: "", exact_len: 0, prefix: "pbkdf2_sha256$" },
    HashDef { name: "MySQL 4.1+", hashcat_mode: "300", john_format: "mysql-sha1",
        notes: "", exact_len: 0, prefix: "*" },
    HashDef { name: "SHA-512 (hex)", hashcat_mode: "1700", john_format: "raw-sha512",
        notes: "", exact_len: 128, prefix: "" },
    HashDef { name: "SHA-256 (hex)", hashcat_mode: "1400", john_format: "raw-sha256",
        notes: "", exact_len: 64, prefix: "" },
    HashDef { name: "SHA-1 (hex)", hashcat_mode: "100", john_format: "raw-sha1",
        notes: "", exact_len: 40, prefix: "" },
    HashDef { name: "NTLM / MD5 (hex, 32 chars)", hashcat_mode: "1000", john_format: "nt",
        notes: "AD context = NTLM (mode 1000), web app = MD5 (mode 0) — check source", exact_len: 32, prefix: "" },
];

fn is_hex(s: &[u8]) -> bool {
    s.iter().all(|&c| matches!(c, b'0'..=b'9' | b'a'..=b'f' | b'A'..=b'F'))
}

fn matches_netntlmv2(h: &[u8]) -> bool {
    // username::domain:challenge(16 hex):response(32 hex):blob
    let parts: Vec<&[u8]> = h.split(|&c| c == b':').collect();
    if parts.len() < 5 { return false; }
    is_hex(parts[3]) && parts[3].len() == 16 && is_hex(parts[4]) && parts[4].len() >= 32
}

// ---- Tool: hash_identify ------------------------------------------------

fn build_result(candidates: &[(&HashDef, bool)]) -> Vec<u8> {
    let mut out = Vec::new();
    out.extend_from_slice(b"[");
    let mut first = true;
    for (def, is_ntlmv2) in candidates {
        if !first { out.extend_from_slice(b","); }
        first = false;
        out.extend_from_slice(b"{\"name\":\"");
        if *is_ntlmv2 { out.extend_from_slice(b"NetNTLMv2"); }
        else { out.extend_from_slice(def.name.as_bytes()); }
        out.extend_from_slice(b"\",\"hashcat_mode\":\"");
        if *is_ntlmv2 { out.extend_from_slice(b"5600"); }
        else { out.extend_from_slice(def.hashcat_mode.as_bytes()); }
        out.extend_from_slice(b"\",\"john_format\":\"");
        if *is_ntlmv2 { out.extend_from_slice(b"netntlmv2"); }
        else { out.extend_from_slice(def.john_format.as_bytes()); }
        out.extend_from_slice(b"\",\"notes\":\"");
        if *is_ntlmv2 { out.extend_from_slice(b"Responder/Inveigh capture"); }
        else { out.extend_from_slice(def.notes.as_bytes()); }
        out.extend_from_slice(b"\"}");
    }
    out.extend_from_slice(b"]");
    out
}

#[no_mangle]
pub extern "C" fn stado_tool_hash_identify(
    args_ptr: *const u8, args_len: u32,
    result_ptr: *mut u8, result_cap: u32,
) -> i32 {
    unsafe { BUMP = 0; } // reset allocator each call

    let args = unsafe { core::slice::from_raw_parts(args_ptr, args_len as usize) };
    let hash = match json_str(args, b"hash") {
        Some(h) => h,
        None => {
            let err = b"{\"error\":\"hash is required\"}";
            return write_json(result_ptr, result_cap, err);
        }
    };

    // Trim whitespace.
    let hash = {
        let start = hash.iter().position(|&c| c != b' ' && c != b'\t' && c != b'\n').unwrap_or(0);
        let end = hash.iter().rposition(|&c| c != b' ' && c != b'\t' && c != b'\n').map(|i| i+1).unwrap_or(0);
        &hash[start..end]
    };

    let hash_lower = {
        let mut v = Vec::with_capacity(hash.len());
        for &b in hash { v.push(b.to_ascii_lowercase()); }
        v
    };

    let mut candidates: Vec<(&HashDef, bool)> = Vec::new();

    // NetNTLMv2 structural check first.
    if matches_netntlmv2(hash) {
        // dummy entry — we mark it via the bool flag
        candidates.push((&HASH_TABLE[2], true));
    }

    for def in HASH_TABLE {
        if def.name == "NetNTLMv2" { continue; } // handled above
        if def.exact_len > 0 && hash.len() != def.exact_len { continue; }
        if def.exact_len > 0 && !is_hex(hash) { continue; }
        if !def.prefix.is_empty() {
            let prefix_lower = def.prefix.as_bytes();
            if hash_lower.len() < prefix_lower.len() { continue; }
            let lower_prefix: Vec<u8> = prefix_lower.iter().map(|b| b.to_ascii_lowercase()).collect();
            if &hash_lower[..lower_prefix.len()] != lower_prefix.as_slice() { continue; }
        }
        candidates.push((def, false));
    }

    let payload = build_result(&candidates);
    write_json(result_ptr, result_cap, &payload)
}
